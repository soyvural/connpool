package connpool

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultNamePrefix = "conn-pool"
	// maxHealthRetries is the number of times Get() will try to find a healthy
	// connection before giving up and creating a new one.
	maxHealthRetries = 3
)

var connPoolCounter atomic.Int64

// Option configures a pool.
type Option func(p *pool) error

// WithName sets the pool name. If not provided, an auto-generated name is used.
func WithName(name string) Option {
	return func(p *pool) error {
		p.name = name
		return nil
	}
}

type pool struct {
	name    string
	cfg     Config
	factory Factory
	conns   chan *conn
	running atomic.Bool
	mu      sync.Mutex
	stats   *stats
	stopCh  chan struct{}
}

// New returns a connection Pool. The factory function is called to create new
// connections. MinSize connections are created immediately.
func New(cfg Config, factory Factory, options ...Option) (Pool, error) {
	if factory == nil {
		return nil, fmt.Errorf("no connection factory provided")
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	p := &pool{
		factory: factory,
		cfg:     cfg,
		conns:   make(chan *conn, cfg.MaxSize),
		stopCh:  make(chan struct{}),
	}
	p.stats = newStats(p)

	for _, opt := range options {
		if err := opt(p); err != nil {
			return nil, err
		}
	}
	if p.name == "" {
		p.name = fmt.Sprintf("%s-%d", defaultNamePrefix, connPoolCounter.Add(1))
	}
	if err := p.start(); err != nil {
		return nil, err
	}
	return p, nil
}

// Get returns a healthy connection from the pool. It blocks until a connection
// is available, the context is cancelled, or the pool is closed.
func (p *pool) Get(ctx context.Context) (c net.Conn, err error) {
	defer p.updateStat(&err)

	if !p.running.Load() {
		return nil, ErrClosed
	}
	return p.get(ctx)
}

// MarkUnusable marks a connection so it will be destroyed on Close() instead
// of being returned to the pool.
func (p *pool) MarkUnusable(c net.Conn) {
	if pc, ok := c.(*conn); ok {
		pc.markUnusable()
	}
}

// Stop shuts down the pool. All idle connections are closed. Active connections
// will be closed when they are returned (via Close()).
func (p *pool) Stop() error {
	if !p.running.CompareAndSwap(true, false) {
		return nil
	}

	close(p.stopCh)

	p.mu.Lock()
	defer p.mu.Unlock()

	p.stats.reset()
	close(p.conns)

	var errMsgs []string
	for c := range p.conns {
		if c.Conn == nil {
			continue
		}
		if err := c.Conn.Close(); err != nil {
			errMsgs = append(errMsgs, err.Error())
		}
	}
	p.conns = nil

	if len(errMsgs) > 0 {
		return fmt.Errorf("errors closing connections: %s", strings.Join(errMsgs, "; "))
	}
	return nil
}

// Name returns the pool name.
func (p *pool) Name() string {
	return p.name
}

// Stats returns a snapshot of pool statistics.
func (p *pool) Stats() Stats {
	return p.stats.snapshot()
}

func (p *pool) start() error {
	if !p.running.CompareAndSwap(false, true) {
		return nil
	}
	if err := p.addConnections(p.cfg.MinSize); err != nil {
		p.running.Store(false)
		return fmt.Errorf("failed to create initial connections: %w", err)
	}
	if interval := p.cfg.evictInterval(); interval > 0 {
		go p.evictLoop(interval)
	}
	return nil
}

// get tries to return a healthy connection, iteratively (no recursion).
func (p *pool) get(ctx context.Context) (net.Conn, error) {
	// Fast path: try non-blocking channel reads.
	for range maxHealthRetries {
		select {
		case c := <-p.conns:
			if p.returnIfHealthy(c) {
				return c, nil
			}
			continue
		default:
			// No idle connection available — fall through to grow/wait.
		}
		break
	}
	return p.growOrWait(ctx)
}

// returnIfHealthy checks health and closes unhealthy connections. Returns true if healthy.
func (p *pool) returnIfHealthy(c *conn) bool {
	if healthy, _ := p.checkHealth(c); healthy {
		return true
	}
	p.destroyConn(c)
	return false
}

// growOrWait tries to create new connections if under capacity,
// otherwise blocks until one becomes available or ctx is done.
func (p *pool) growOrWait(ctx context.Context) (net.Conn, error) {
	// Try to grow the pool.
	currentSize := int(p.stats.size.Load())
	if currentSize < p.cfg.MaxSize {
		n := p.cfg.Increment
		if n+currentSize > p.cfg.MaxSize {
			n = p.cfg.MaxSize - currentSize
		}
		if err := p.addConnections(n); err != nil {
			return nil, err
		}
		// Try to grab one of the newly created connections.
		select {
		case c := <-p.conns:
			if p.returnIfHealthy(c) {
				return c, nil
			}
			// Unhealthy new connection — unusual, but don't recurse.
			return nil, ErrExhausted
		default:
			// Another goroutine took it.
			return nil, ErrExhausted
		}
	}

	// Pool is at max capacity — wait for a connection to be returned.
	return p.waitForConn(ctx)
}

func (p *pool) waitForConn(ctx context.Context) (net.Conn, error) {
	p.stats.waitCount.Add(1)
	start := time.Now()
	defer func() {
		p.stats.waitTimeNanos.Add(int64(time.Since(start)))
	}()

	select {
	case c := <-p.conns:
		if c == nil {
			return nil, ErrClosed
		}
		if p.returnIfHealthy(c) {
			return c, nil
		}
		// Connection was unhealthy. Create a replacement.
		if err := p.addConnections(1); err != nil {
			return nil, err
		}
		select {
		case fresh := <-p.conns:
			return fresh, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// checkHealth validates a connection. Returns (true, "") if healthy.
func (p *pool) checkHealth(c *conn) (bool, string) {
	if c.Conn == nil {
		return false, "nil"
	}
	if c.isUnusable() {
		return false, "unusable"
	}
	if c.isExpired() {
		p.stats.lifetimeClosed.Add(1)
		return false, "lifetime"
	}
	if c.isIdle(p.cfg.IdleTimeout) {
		p.stats.idleClosed.Add(1)
		return false, "idle"
	}
	if p.cfg.Ping != nil {
		if err := p.cfg.Ping(c.Conn); err != nil {
			p.stats.pingFailed.Add(1)
			return false, "ping"
		}
	}
	return true, ""
}

func (p *pool) put(c *conn) error {
	if c.isUnusable() || !p.running.Load() {
		p.stats.size.Add(-1)
		return c.Conn.Close()
	}
	c.stampLastUsed()
	select {
	case p.conns <- c:
		return nil
	default:
		// Channel full — close the connection.
		p.stats.size.Add(-1)
		return c.Conn.Close()
	}
}

func (p *pool) addConnections(n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for range n {
		if int(p.stats.size.Load()) >= p.cfg.MaxSize {
			return nil
		}
		netConn, err := p.factory()
		if err != nil {
			return fmt.Errorf("factory error: %w", err)
		}
		c := newConn(netConn, p, p.cfg.maxLifetimeWithJitter())
		p.stats.size.Add(1)
		// Non-blocking send: if channel is somehow full, destroy.
		select {
		case p.conns <- c:
		default:
			p.stats.size.Add(-1)
			_ = netConn.Close()
		}
	}
	return nil
}

// destroyConn closes a connection and decrements the size counter.
func (p *pool) destroyConn(c *conn) {
	p.stats.size.Add(-1)
	if c.Conn != nil {
		_ = c.Conn.Close()
	}
}

func (p *pool) available() int {
	return len(p.conns)
}

func (p *pool) updateStat(err *error) {
	p.stats.request.Add(1)
	if *err == nil {
		p.stats.success.Add(1)
	}
}

// evictLoop runs periodically to remove stale connections and maintain min size.
func (p *pool) evictLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.evict()
		}
	}
}

func (p *pool) evict() {
	if !p.running.Load() {
		return
	}

	// Drain the channel, check each connection, put healthy ones back.
	n := len(p.conns)
	for range n {
		select {
		case c := <-p.conns:
			if healthy, _ := p.checkHealth(c); !healthy {
				p.destroyConn(c)
				continue
			}
			select {
			case p.conns <- c:
			default:
				p.destroyConn(c)
			}
		default:
			return
		}
	}

	// Maintain min idle count.
	current := int(p.stats.size.Load())
	if current < p.cfg.MinSize {
		_ = p.addConnections(p.cfg.MinSize - current)
	}
}
