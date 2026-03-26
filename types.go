package connpool

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"time"
)

var (
	ErrClosed    = fmt.Errorf("pool is closed")
	ErrExhausted = fmt.Errorf("pool is exhausted")
	ErrTimeout   = fmt.Errorf("pool get timed out")
)

// Factory creates a new net.Conn. It is called when the pool needs to grow.
type Factory func() (net.Conn, error)

// PingFunc checks whether a connection is still alive.
// Return nil if the connection is healthy, or an error to discard it.
type PingFunc func(net.Conn) error

// Pool is a thread-safe connection pool for net.Conn.
type Pool interface {
	// Name returns the pool name.
	Name() string

	// Get returns a healthy connection from the pool.
	// It blocks until a connection is available, the context is cancelled,
	// or the pool is closed.
	Get(ctx context.Context) (net.Conn, error)

	// Stop shuts down the pool and closes all connections.
	Stop() error

	// Stats returns a snapshot of pool statistics.
	Stats() Stats

	// MarkUnusable marks a connection so it will be destroyed instead of
	// returned to the pool on Close().
	MarkUnusable(conn net.Conn)
}

// Config controls pool behavior.
type Config struct {
	// MinSize is the number of connections created on startup and maintained
	// by the background evictor. Must be >= 0.
	MinSize int

	// MaxSize is the maximum number of connections (idle + active). Must be > 0.
	MaxSize int

	// Increment is how many connections to create at once when the pool needs
	// to grow. Must be >= 1 and <= MaxSize - MinSize.
	Increment int

	// IdleTimeout is how long a connection can sit idle before being evicted.
	// Zero means no idle timeout.
	IdleTimeout time.Duration

	// MaxLifetime is the maximum duration a connection can live since creation.
	// Connections older than this are closed on Get() and by the evictor.
	// A jitter of up to 10% is applied to prevent thundering herd.
	// Zero means no max lifetime.
	MaxLifetime time.Duration

	// Ping is an optional health check function called on Get() before
	// returning a connection. If it returns an error, the connection is
	// discarded and a new one is tried.
	Ping PingFunc

	// EvictInterval is how often the background evictor runs.
	// Zero defaults to 30 seconds. Set to -1 to disable the evictor.
	EvictInterval time.Duration
}

func (c *Config) validate() error {
	if c.MinSize < 0 || c.MaxSize <= 0 || c.MinSize > c.MaxSize {
		return fmt.Errorf("invalid configuration: MinSize=%d MaxSize=%d (need 0 <= MinSize <= MaxSize, MaxSize > 0)", c.MinSize, c.MaxSize)
	}
	if c.Increment < 1 {
		return fmt.Errorf("invalid configuration: Increment must be >= 1, got %d", c.Increment)
	}
	if c.MinSize > 0 && c.MinSize < c.MaxSize && c.Increment > (c.MaxSize-c.MinSize) {
		return fmt.Errorf("invalid configuration: Increment=%d exceeds MaxSize-MinSize=%d", c.Increment, c.MaxSize-c.MinSize)
	}
	return nil
}

func (c *Config) evictInterval() time.Duration {
	if c.EvictInterval > 0 {
		return c.EvictInterval
	}
	if c.EvictInterval < 0 {
		return 0 // disabled
	}
	return 30 * time.Second
}

// maxLifetimeWithJitter returns the max lifetime with up to 10% random jitter.
// This prevents thundering herd — all connections created at the same time
// won't expire at the same moment.
func (c *Config) maxLifetimeWithJitter() time.Duration {
	if c.MaxLifetime <= 0 {
		return 0
	}
	jitter := time.Duration(rand.Int64N(int64(c.MaxLifetime) / 10))
	return c.MaxLifetime + jitter
}

// Stats is a snapshot of pool statistics.
type Stats interface {
	// Available is the number of idle connections in the pool.
	Available() int

	// Active is the number of connections currently checked out.
	Active() int

	// Size is the total number of connections (idle + active).
	Size() int

	// Request is the total number of Get() calls.
	Request() int

	// Success is the total number of successful Get() calls.
	Success() int

	// IdleClosed is the total number of connections closed due to idle timeout.
	IdleClosed() int

	// LifetimeClosed is the total number of connections closed due to max lifetime.
	LifetimeClosed() int

	// PingFailed is the total number of connections discarded due to ping failure.
	PingFailed() int

	// WaitCount is the total number of Get() calls that had to wait for a connection.
	WaitCount() int

	// WaitTime is the cumulative time spent waiting for connections.
	WaitTime() time.Duration
}
