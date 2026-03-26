// Example: Round-robin load balancer across multiple backends using connpool.
//
// Demonstrates using multiple independent pools — one per backend server —
// with round-robin selection. Each pool independently manages health checks,
// idle timeouts, and connection lifecycle.
//
// Start two echo servers:
//
//	ncat -l -k -p 9001 --sh-exec "echo backend-1"
//	ncat -l -k -p 9002 --sh-exec "echo backend-2"
//
// Then run this example:
//
//	go run ./examples/load-balancer
package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/soyvural/connpool"
)

type loadBalancer struct {
	pools   []connpool.Pool
	counter atomic.Int64
}

func (lb *loadBalancer) get(ctx context.Context) (net.Conn, error) {
	idx := lb.counter.Add(1) % int64(len(lb.pools))
	return lb.pools[idx].Get(ctx)
}

func (lb *loadBalancer) stop() {
	for _, p := range lb.pools {
		p.Stop()
	}
}

func (lb *loadBalancer) printStats() {
	for _, p := range lb.pools {
		s := p.Stats()
		fmt.Printf("  %s: size=%d available=%d requests=%d success=%d\n",
			p.Name(), s.Size(), s.Available(), s.Request(), s.Success())
	}
}

func main() {
	backends := []string{"localhost:9001", "localhost:9002"}

	lb := &loadBalancer{}
	for i, addr := range backends {
		addr := addr
		cfg := connpool.Config{
			MinSize:     2,
			MaxSize:     10,
			Increment:   2,
			IdleTimeout: 30 * time.Second,
		}
		factory := func() (net.Conn, error) {
			return net.DialTimeout("tcp", addr, 3*time.Second)
		}
		pool, err := connpool.New(cfg, factory, connpool.WithName(fmt.Sprintf("backend-%d(%s)", i+1, addr)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to create pool for %s: %v\n", addr, err)
			os.Exit(1)
		}
		lb.pools = append(lb.pools, pool)
	}
	defer lb.stop()

	// Send 8 requests, round-robin across backends.
	for i := range 8 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)

		conn, err := lb.get(ctx)
		if err != nil {
			cancel()
			fmt.Fprintf(os.Stderr, "[%d] get: %v\n", i, err)
			continue
		}

		fmt.Fprintf(conn, "request-%d\n", i)
		reply, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			conn.Close()
			cancel()
			fmt.Fprintf(os.Stderr, "[%d] read: %v\n", i, err)
			continue
		}
		conn.Close()
		cancel()

		fmt.Printf("[%d] %s\n", i, strings.TrimSpace(reply))
	}

	fmt.Println("\nPool stats:")
	lb.printStats()
}
