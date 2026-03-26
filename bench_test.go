package connpool

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

type testBenchServer struct {
	listener net.Listener
}

func benchServer(b *testing.B) (*testBenchServer, Factory) {
	b.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	s := &testBenchServer{listener: l}
	go s.accept()
	b.Cleanup(s.stop)

	addr := l.Addr().String()
	factory := func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}
	return s, factory
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

func (s *testBenchServer) stop() { _ = s.listener.Close() }

// BenchmarkGetPut_Sequential measures Get+Close throughput with no contention.
func BenchmarkGetPut_Sequential(b *testing.B) {
	_, factory := benchServer(b)

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
	defer func() { _ = p.Stop() }()

	ctx := context.Background()
	b.ResetTimer()

	for range b.N {
		c, err := p.Get(ctx)
		if err != nil {
			b.Fatalf("Get: %v", err)
		}
		_ = c.Close()
	}
}

// BenchmarkGetPut_Parallel measures throughput under concurrent load.
func BenchmarkGetPut_Parallel(b *testing.B) {
	_, factory := benchServer(b)

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
	defer func() { _ = p.Stop() }()

	ctx := context.Background()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c, err := p.Get(ctx)
			if err != nil {
				continue
			}
			_ = c.Close()
		}
	})
}

// BenchmarkGetPut_WithPing measures overhead of the health check on each Get.
func BenchmarkGetPut_WithPing(b *testing.B) {
	_, factory := benchServer(b)

	cfg := Config{
		MinSize:       5,
		MaxSize:       10,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
		Ping: func(c net.Conn) error {
			_ = c.SetReadDeadline(time.Now().Add(time.Microsecond))
			buf := make([]byte, 1)
			_, _ = c.Read(buf)
			_ = c.SetReadDeadline(time.Time{})
			return nil
		},
	}
	p, err := New(cfg, factory)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	ctx := context.Background()
	b.ResetTimer()

	for range b.N {
		c, err := p.Get(ctx)
		if err != nil {
			b.Fatalf("Get: %v", err)
		}
		_ = c.Close()
	}
}

// BenchmarkGetPut_Contended measures behavior under full pool contention.
func BenchmarkGetPut_Contended(b *testing.B) {
	_, factory := benchServer(b)

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
	defer func() { _ = p.Stop() }()

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
				_ = c.Close()
			}
		}()
	}
	wg.Wait()
}
