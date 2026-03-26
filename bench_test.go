package connpool

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

func benchServer(b *testing.B) (*testBenchServer, Factory) {
	b.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	s := &testBenchServer{listener: l, done: make(chan struct{})}
	go s.accept()

	addr := l.Addr().String()
	factory := func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}
	return s, factory
}

type testBenchServer struct {
	listener net.Listener
	done     chan struct{}
}

func (s *testBenchServer) accept() {
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			buf := make([]byte, 1024)
			for {
				if _, err := c.Read(buf); err != nil {
					return
				}
			}
		}()
	}
}

func (s *testBenchServer) stop() {
	close(s.done)
	s.listener.Close()
}

// BenchmarkGetPut measures the throughput of Get+Close (return to pool) with
// no contention (sequential, single goroutine).
func BenchmarkGetPut_Sequential(b *testing.B) {
	srv, factory := benchServer(b)
	defer srv.stop()

	cfg := Config{
		MinSize:       5,
		MaxSize:       10,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
	}
	p, err := New(cfg, factory)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer p.Stop()

	ctx := context.Background()
	b.ResetTimer()

	for range b.N {
		c, err := p.Get(ctx)
		if err != nil {
			b.Fatalf("Get: %v", err)
		}
		c.Close()
	}
}

// BenchmarkGetPut_Parallel measures throughput under concurrent load.
func BenchmarkGetPut_Parallel(b *testing.B) {
	srv, factory := benchServer(b)
	defer srv.stop()

	cfg := Config{
		MinSize:       5,
		MaxSize:       20,
		Increment:     2,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
	}
	p, err := New(cfg, factory)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer p.Stop()

	ctx := context.Background()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c, err := p.Get(ctx)
			if err != nil {
				continue
			}
			c.Close()
		}
	})
}

// BenchmarkGetPut_WithPing measures overhead of the health check on each Get.
func BenchmarkGetPut_WithPing(b *testing.B) {
	srv, factory := benchServer(b)
	defer srv.stop()

	cfg := Config{
		MinSize:       5,
		MaxSize:       10,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
		Ping: func(c net.Conn) error {
			// Lightweight ping: set a short read deadline to check if conn is alive.
			c.SetReadDeadline(time.Now().Add(time.Microsecond))
			buf := make([]byte, 1)
			c.Read(buf)
			c.SetReadDeadline(time.Time{})
			return nil
		},
	}
	p, err := New(cfg, factory)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer p.Stop()

	ctx := context.Background()
	b.ResetTimer()

	for range b.N {
		c, err := p.Get(ctx)
		if err != nil {
			b.Fatalf("Get: %v", err)
		}
		c.Close()
	}
}

// BenchmarkGetPut_Contended measures behavior when pool is fully utilized
// and goroutines must wait for connections to be returned.
func BenchmarkGetPut_Contended(b *testing.B) {
	srv, factory := benchServer(b)
	defer srv.stop()

	cfg := Config{
		MinSize:       2,
		MaxSize:       2,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
	}
	p, err := New(cfg, factory)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer p.Stop()

	ctx := context.Background()
	b.ResetTimer()

	var wg sync.WaitGroup
	workers := 8
	perWorker := b.N / workers
	if perWorker < 1 {
		perWorker = 1
	}

	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for range perWorker {
				c, err := p.Get(ctx)
				if err != nil {
					continue
				}
				c.Close()
			}
		}()
	}
	wg.Wait()
}
