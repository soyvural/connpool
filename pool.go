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
	// maxPingRetries is the number of times Get() will try to find a healthy
	// connection before giving up and creating a new one.
	maxPingRetries = 3
)

var connPoolCounter = newCounter()

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
	running int32
	mu      sync.RWMutex
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
		p.name = fmt.Sprintf("%s-%d", defaultNamePrefix, connPoolCounter.inc())
	}
	if err := p.start(); err != nil {
		return nil, err
	}
	return p, nil
}

// Get returns a healthy connection from the pool. It blocks until a connection
// is available, the context is cancelled, or the pool is closed.
func (p *pool) Get(ctx context.Context) (conn net.Conn, err error) {
	defer p.updateStat(&err)

	if p.conns == nil || atomic.LoadInt32(&p.running) == 0 {
		return nil, ErrClosed
	}
	return p.get(ctx)
}

// MarkUnusable marks a connection so it will be destroyed on Close() instead
// of being returned to the pool.
func (p *pool) MarkUnusable(c net.Conn) {
	if c, ok := c.(*conn); ok {
		c.markUnusable()
	}
}

// Stop shuts down the pool. All idle connections are closed. Active connections
// will be closed when they are returned (via Close()).
func (p *pool) Stop() error {
	if !atomic.CompareAndSwapInt32(&p.running, 1, 0) {
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
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stats.snapshot()
}

func (p *pool) start() error {
	if !atomic.CompareAndSwapInt32(&p.running, 0, 1) {
		return nil
	}
	if err := p.addConnections(p.cfg.MinSize); err != nil {
		atomic.StoreInt32(&p.running, 0)
		return fmt.Errorf("failed to create initial connections: %w", err)
	}
	if interval := p.cfg.evictInterval(); interval > 0 {
		go p.evictLoop(interval)
	}
	return nil
}

func (p *pool) get(ctx context.Context) (net.Conn, error) {
	// Fast path: try non-blocking channel read.
	for range maxPingRetries {
		select {
		case c := <-p.conns:
			if healthy, reason := p.checkHealth(c); !healthy {
				p.closeConn(c, reason)
				continue
			}
			return c, nil
		default:
			// No idle connection available, try to grow.
			return p.tryGetOrWait(ctx)
		}
	}
	// All retries exhausted from stale connections, grow or wait.
	return p.tryGetOrWait(ctx)
}

// tryGetOrWait tries to create new connections if under capacity,
// otherwise blocks until one becomes available or ctx is done.
func (p *pool) tryGetOrWait(ctx context.Context) (net.Conn, error) {
	if p.stats.size.val() < p.cfg.MaxSize {
		n := p.cfg.Increment
		if n+p.stats.size.val() > p.cfg.MaxSize {
			n = p.cfg.MaxSize - p.stats.size.val()
		}
		if err := p.addConnections(n); err != nil {
			return nil, err
		}
		// Try again after growing.
		select {
		case c := <-p.conns:
			if healthy, reason := p.checkHealth(c); !healthy {
				p.closeConn(c, reason)
				return p.tryGetOrWait(ctx)
			}
			return c, nil
		default:
			return nil, ErrExhausted
		}
	}

	// Pool is at max capacity — wait for a connection to be returned.
	p.stats.waitCount.inc()
	start := time.Now()
	defer func() {
		p.stats.waitTimeNanos.add(int64(time.Since(start)))
	}()

	select {
	case c := <-p.conns:
		if c == nil {
			return nil, ErrClosed
		}
		if healthy, reason := p.checkHealth(c); !healthy {
			p.closeConn(c, reason)
			// Closing freed a slot, try creating a fresh one.
			if err := p.addConnections(1); err != nil {
				return nil, err
			}
			select {
			case fresh := <-p.conns:
				return fresh, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return c, nil
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
		p.stats.lifetimeClosed.inc()
		return false, "lifetime"
	}
	if c.isIdle(p.cfg.IdleTimeout) {
		p.stats.idleClosed.inc()
		return false, "idle"
	}
	if p.cfg.Ping != nil {
		if err := p.cfg.Ping(c.Conn); err != nil {
			p.stats.pingFailed.inc()
			return false, "ping"
		}
	}
	return true, ""
}

func (p *pool) put(c *conn) error {
	if c.isUnusable() || atomic.LoadInt32(&p.running) == 0 {
		p.stats.size.dec()
		return c.Conn.Close()
	}
	c.stampLastUsed()
	select {
	case p.conns <- c:
		return nil
	default:
		// Channel full — close the connection.
		p.stats.size.dec()
		return c.Conn.Close()
	}
}

func (p *pool) addConnections(size int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for range size {
		if p.stats.size.val() >= p.cfg.MaxSize {
			return nil
		}
		netConn, err := p.factory()
		if err != nil {
			return fmt.Errorf("factory error: %w", err)
		}
		c := newConn(netConn, p, p.cfg.maxLifetimeWithJitter())
		c.stampLastUsed()
		p.conns <- c
		p.stats.size.inc()
	}
	return nil
}

func (p *pool) closeConn(c *conn, _ string) {
	p.stats.size.dec()
	if c.Conn != nil {
		_ = c.Conn.Close()
	}
}

func (p *pool) available() int {
	return len(p.conns)
}

func (p *pool) updateStat(err *error) {
	p.stats.request.inc()
	if *err == nil {
		p.stats.success.inc()
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
	if atomic.LoadInt32(&p.running) == 0 {
		return
	}

	// Drain the channel, check each connection, put healthy ones back.
	n := len(p.conns)
	for range n {
		select {
		case c := <-p.conns:
			if healthy, reason := p.checkHealth(c); !healthy {
				p.closeConn(c, reason)
				continue
			}
			// Still healthy, put back.
			select {
			case p.conns <- c:
			default:
				p.closeConn(c, "overflow")
			}
		default:
			return
		}
	}

	// Maintain min idle count.
	current := p.stats.size.val()
	if current < p.cfg.MinSize {
		_ = p.addConnections(p.cfg.MinSize - current)
	}
}
