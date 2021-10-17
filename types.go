package connpool

import (
	"fmt"
	"net"
	"time"
)

var (
	ErrClosed = fmt.Errorf("pool is closed")
)

type Factory func() (net.Conn, error)

type Pool interface {
	Name() string
	Get() (net.Conn, error)
	Stop() error
	Stats() Stats
	MarkUnusable(conn net.Conn)
}

type Config struct {
	MinSize     int
	MaxSize     int
	Increment   int
	IdleTimeout time.Duration
}

type Stats interface {
	// Available connections size in the pool.
	Available() int

	// Active connections used by consumers.
	Active() int

	// Request total number of get connection attempts.
	Request() int

	// Success total number of successfully completed get connection.
	Success() int
}
