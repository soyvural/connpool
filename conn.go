package connpool

import (
	"fmt"
	"net"
	"sync"
)

type putter interface {
	put(c *conn) error
}

type conn struct {
	// unix epoch Nanoseconds
	lastUsed int64
	p        putter
	unUsable bool
	mu       sync.RWMutex
	net.Conn
}

func (c *conn) Close() (e error) {
	if c.Conn == nil {
		return fmt.Errorf("invalid conn, it is nil")
	}
	return c.p.put(c)
}

func newConn(c net.Conn, p putter) *conn {
	return &conn{Conn: c, p: p}
}

func (c *conn) markUnusable() {
	c.mu.Lock()
	c.unUsable = true
	c.mu.Unlock()
}

func (c *conn) isUnUsable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.unUsable
}
