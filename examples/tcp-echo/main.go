// Example: TCP echo client using connpool.
//
// Demonstrates basic pool usage: Get a connection, send data, read the
// response, and let Close() return the connection to the pool.
//
// Run a TCP echo server first:
//
//	ncat -l -k -p 9090 --sh-exec "cat"
//
// Then run this example:
//
//	go run ./examples/tcp-echo
package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/soyvural/connpool"
)

func main() {
	cfg := connpool.Config{
		MinSize:     2,
		MaxSize:     10,
		Increment:   2,
		IdleTimeout: 30 * time.Second,
		MaxLifetime: 5 * time.Minute,
	}

	factory := func() (net.Conn, error) {
		return net.DialTimeout("tcp", "localhost:9090", 5*time.Second)
	}

	pool, err := connpool.New(cfg, factory, connpool.WithName("echo-pool"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create pool: %v\n", err)
		os.Exit(1)
	}
	defer pool.Stop()

	// Send 5 messages, reusing pooled connections.
	for i := range 5 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		conn, err := pool.Get(ctx)
		if err != nil {
			cancel()
			fmt.Fprintf(os.Stderr, "get failed: %v\n", err)
			continue
		}

		msg := fmt.Sprintf("hello #%d\n", i+1)
		fmt.Fprint(conn, msg)

		reply, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			pool.MarkUnusable(conn)
			conn.Close()
			cancel()
			fmt.Fprintf(os.Stderr, "read failed: %v\n", err)
			continue
		}
		conn.Close() // returns to pool

		fmt.Printf("sent: %q  got: %q\n", msg[:len(msg)-1], reply[:len(reply)-1])
		cancel()
	}

	stats := pool.Stats()
	fmt.Printf("\nPool stats: size=%d active=%d available=%d requests=%d success=%d\n",
		stats.Size(), stats.Active(), stats.Available(), stats.Request(), stats.Success())
}
