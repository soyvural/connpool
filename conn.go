package connpool

import (
	"fmt"
	"net"
	"sync"
	"time"
)

type putter interface {
	put(c *conn) error
}

type conn struct {
	net.Conn

	p       putter
	created time.Time
	// maxLifetime is the jittered max lifetime for this specific connection.
	maxLifetime time.Duration

	mu       sync.RWMutex
	lastUsed int64 // unix epoch nanoseconds
	unusable bool
}

func newConn(c net.Conn, p putter, maxLifetime time.Duration) *conn {
	return &conn{
		Conn:        c,
		p:           p,
		created:     time.Now(),
		maxLifetime: maxLifetime,
	}
}

// Close returns the connection to the pool (or destroys it if marked unusable).
func (c *conn) Close() error {
	if c.Conn == nil {
		return fmt.Errorf("invalid conn, it is nil")
	}
	return c.p.put(c)
}

func (c *conn) markUnusable() {
	c.mu.Lock()
	c.unusable = true
	c.mu.Unlock()
}

func (c *conn) isUnusable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.unusable
}

// isExpired returns true if the connection has exceeded its max lifetime.
func (c *conn) isExpired() bool {
	if c.maxLifetime <= 0 {
		return false
	}
	return time.Since(c.created) > c.maxLifetime
}

// isIdle returns true if the connection has been idle longer than the given timeout.
func (c *conn) isIdle(timeout time.Duration) bool {
	if timeout <= 0 {
		return false
	}
	lu := c.lastUsed
	if lu == 0 {
		return false
	}
	return lu < time.Now().Add(-timeout).UTC().UnixNano()
}

func (c *conn) stampLastUsed() {
	c.lastUsed = time.Now().UTC().UnixNano()
}
