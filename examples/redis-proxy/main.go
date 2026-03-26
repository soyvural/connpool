// Example: Simple Redis PING proxy using connpool.
//
// Demonstrates connection pooling with a health check (Ping). The pool
// maintains warm connections to Redis. Each request gets a pre-validated
// connection, sends a PING, and returns the PONG response.
//
// Requires a running Redis server on localhost:6379:
//
//	docker run -d -p 6379:6379 redis:alpine
//
// Then run this example:
//
//	go run ./examples/redis-proxy
package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/soyvural/connpool"
)

func main() {
	cfg := connpool.Config{
		MinSize:       3,
		MaxSize:       20,
		Increment:     3,
		IdleTimeout:   1 * time.Minute,
		MaxLifetime:   10 * time.Minute,
		EvictInterval: 15 * time.Second,
		Ping:          redisPing,
	}

	factory := func() (net.Conn, error) {
		return net.DialTimeout("tcp", "localhost:6379", 3*time.Second)
	}

	pool, err := connpool.New(cfg, factory, connpool.WithName("redis-pool"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create pool: %v\n", err)
		os.Exit(1)
	}
	defer pool.Stop()

	// Simulate 10 concurrent PING requests.
	for i := range 10 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)

		conn, err := pool.Get(ctx)
		if err != nil {
			cancel()
			fmt.Fprintf(os.Stderr, "[%d] get failed: %v\n", i, err)
			continue
		}

		fmt.Fprint(conn, "*1\r\n$4\r\nPING\r\n") // RESP PING command
		reply, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			pool.MarkUnusable(conn)
			conn.Close()
			cancel()
			fmt.Fprintf(os.Stderr, "[%d] read failed: %v\n", i, err)
			continue
		}
		conn.Close() // returns to pool
		cancel()

		fmt.Printf("[%d] Redis says: %s\n", i, strings.TrimSpace(reply))
	}

	stats := pool.Stats()
	fmt.Printf("\nPool: size=%d idle=%d active=%d | idle_closed=%d lifetime_closed=%d ping_failed=%d\n",
		stats.Size(), stats.Available(), stats.Active(),
		stats.IdleClosed(), stats.LifetimeClosed(), stats.PingFailed())
}

// redisPing sends an inline PING and checks for +PONG.
func redisPing(c net.Conn) error {
	c.SetDeadline(time.Now().Add(time.Second))
	defer c.SetDeadline(time.Time{})

	fmt.Fprint(c, "PING\r\n")
	reply, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return fmt.Errorf("ping read: %w", err)
	}
	if !strings.Contains(reply, "PONG") {
		return fmt.Errorf("unexpected reply: %s", reply)
	}
	return nil
}
