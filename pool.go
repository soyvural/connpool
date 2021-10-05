package connpool

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultNamePrefix = "conn-pool"
)

var (
	connPoolCounter = newCounter()
)

type Option func(p *pool) error

// WithName is an option and used for naming the pool.
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
}

// New returns a connection Pool.
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

	}
	return p, nil
}

// Get returns a connection.
// Make sure pool is not stopped before calling otherwise the process will be received a ErrClosed error.
// You should close conn object ASAP when it is done.
func (p *pool) Get() (conn net.Conn, err error) {
	defer p.updateStat(&err)

	if p.conns == nil || atomic.LoadInt32(&p.running) == 0 {
		return nil, ErrClosed
	}
	return p.get()
}

// MarkUnusable sets conn unusable and the connection will not be used anymore.
// So, if your process gets an error on the network call MarkUnusable method before closing it.
func (p *pool) MarkUnusable(c net.Conn) {
	if c, ok := c.(*conn); ok {
		c.markUnusable()
		p.stats.size.dec()
	}
}

// Stop terminates the pool. Once you called it you can not resume the pool for now.
func (p *pool) Stop() error {
	if atomic.CompareAndSwapInt32(&p.running, 1, 0) {
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
				errMsgs = append(errMsgs, fmt.Sprintf("error: %v", err))
			}
		}
		p.conns = nil

		if len(errMsgs) > 0 {
			return fmt.Errorf(strings.Join(errMsgs, "\n"))
		}
	}
	return nil
}

// Name returns the pool name.
// If you do not provide name param while creating the pool then a name starts with "conn-pool" is assigned.
func (p *pool) Name() string {
	return p.name
}

// Stats returns statistical info of pool.
// It might be extended in the future.
func (p *pool) Stats() Stats {
	return p.stats
}

func (p *pool) start() error {
	if atomic.CompareAndSwapInt32(&p.running, 0, 1) {
		return p.addConnections(p.cfg.MinSize)
	}
	return nil
}

func (p *pool) get() (net.Conn, error) {
	select {
	case c := <-p.conns:
		if c.lastUsed > 0 && c.lastUsed < time.Now().Add(-p.cfg.IdleTimeout).UTC().UnixNano() {
			defer c.Conn.Close()
			p.stats.size.dec()
			return p.get()
		}
		return c, nil
	default:
		return p.tryGet()
	}
}

func (p *pool) put(c *conn) error {
	if c.isUnUsable() {
		return c.Conn.Close()
	}
	select {
	case p.conns <- c:
		c.lastUsed = time.Now().UTC().UnixNano()
		return nil
	default:
		// if channel is full then close conn.
		p.stats.size.dec()
		return c.Conn.Close()
	}
}

func (p *pool) tryGet() (net.Conn, error) {
	if p.stats.size.val() < p.cfg.MaxSize {
		n := p.cfg.Increment
		if n+p.stats.size.val() > p.cfg.MaxSize {
			n = p.cfg.MaxSize - p.stats.size.val()
		}
		if err := p.addConnections(n); err != nil {
			return nil, err
		}
		return p.get()
	}
	return nil, fmt.Errorf("could not retrieve any connection")
}

func (p *pool) addConnections(size int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := 0; i < size; i++ {
		if p.stats.size.val() >= p.cfg.MaxSize {
			return nil
		}
		c, err := p.factory()
		if err != nil {
			return err
		}
		p.conns <- newConn(c, p)
		p.stats.size.inc()
	}
	return nil
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

func (c *Config) validate() error {
	if c.MinSize < 0 || c.MaxSize <= 0 || c.MinSize > c.MaxSize {
		return fmt.Errorf("invalid configuration, please check min and max values")
	}
	if c.Increment > (c.MaxSize - c.MinSize) {
		return fmt.Errorf("increment value can be the difference between min, max at most")
	}
	return nil
}
