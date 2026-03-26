package connpool

import (
	"fmt"
	"net"
	"sync/atomic"
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

	lastUsed atomic.Int64 // unix epoch nanoseconds
	unusable atomic.Bool
}

func newConn(c net.Conn, p putter, maxLifetime time.Duration) *conn {
	cn := &conn{
		Conn:        c,
		p:           p,
		created:     time.Now(),
		maxLifetime: maxLifetime,
	}
	cn.stampLastUsed()
	return cn
}

// Close returns the connection to the pool (or destroys it if marked unusable).
func (c *conn) Close() error {
	if c.Conn == nil {
		return fmt.Errorf("invalid conn, it is nil")
	}
	return c.p.put(c)
}

func (c *conn) markUnusable() {
	c.unusable.Store(true)
}

func (c *conn) isUnusable() bool {
	return c.unusable.Load()
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
	lu := c.lastUsed.Load()
	if lu == 0 {
		return false
	}
	idleSince := time.Since(time.Unix(0, lu))
	return idleSince > timeout
}

func (c *conn) stampLastUsed() {
	c.lastUsed.Store(time.Now().UnixNano())
}
