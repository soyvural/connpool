package connpool

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- test helpers ---

type testServer struct {
	listener net.Listener
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test server: %v", err)
	}
	s := &testServer{listener: l}
	go s.accept()
	t.Cleanup(s.stop)
	return s
}

func (s *testServer) accept() {
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			_, _ = io.Copy(io.Discard, c)
		}()
	}
}

func (s *testServer) addr() string { return s.listener.Addr().String() }

func (s *testServer) stop() { _ = s.listener.Close() }

func testFactory(t *testing.T, srv *testServer) Factory {
	t.Helper()
	return func() (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	}
}

func nilFactory() (net.Conn, error) { return nil, nil }

// --- Config validation ---

func TestNew(t *testing.T) {
	tests := []struct {
		desc    string
		cfg     Config
		wantErr bool
	}{
		{
			desc:    "zero MaxSize",
			cfg:     Config{},
			wantErr: true,
		},
		{
			desc: "MinSize > MaxSize",
			cfg: Config{
				MinSize:   2,
				MaxSize:   1,
				Increment: 1,
			},
			wantErr: true,
		},
		{
			desc: "Increment too large",
			cfg: Config{
				MinSize:   6,
				MaxSize:   10,
				Increment: 5,
			},
			wantErr: true,
		},
		{
			desc: "Increment zero",
			cfg: Config{
				MinSize:   0,
				MaxSize:   10,
				Increment: 0,
			},
			wantErr: true,
		},
		{
			desc: "valid config",
			cfg: Config{
				MinSize:     5,
				MaxSize:     30,
				Increment:   1,
				IdleTimeout: time.Minute,
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := New(tc.cfg, nilFactory)
			if (err != nil) != tc.wantErr {
				t.Fatalf("New(): wantErr=%v, got err=%v", tc.wantErr, err)
			}
		})
	}
}

func TestNew_NilFactory(t *testing.T) {
	_, err := New(Config{MaxSize: 5, Increment: 1}, nil)
	if err == nil {
		t.Fatal("expected error for nil factory")
	}
}

// --- Get ---

func TestGet(t *testing.T) {
	srv := newTestServer(t)

	cfg := Config{
		MinSize:       1,
		MaxSize:       5,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	c, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if c == nil {
		t.Fatal("Get returned nil conn")
	}
	_ = c.Close()
}

func TestGet_ContextCancelled(t *testing.T) {
	srv := newTestServer(t)

	cfg := Config{
		MinSize:       1,
		MaxSize:       1,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	// Take the only connection.
	c, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Pool is exhausted. A cancelled context should return quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = p.Get(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	_ = c.Close()
}

func TestGet_ClosedPool(t *testing.T) {
	p, err := New(Config{MaxSize: 5, Increment: 1, IdleTimeout: time.Minute, EvictInterval: -1}, nilFactory)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = p.Stop()

	_, err = p.Get(context.Background())
	if err != ErrClosed {
		t.Fatalf("expected ErrClosed, got: %v", err)
	}
}

func TestGet_GrowsOnDemand(t *testing.T) {
	srv := newTestServer(t)

	cfg := Config{
		MinSize:       0,
		MaxSize:       5,
		Increment:     2,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	ctx := context.Background()
	var conns []net.Conn
	for range 5 {
		c, err := p.Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		conns = append(conns, c)
	}

	if got := p.Stats().Size(); got != 5 {
		t.Fatalf("Size: got %d, want 5", got)
	}

	for _, c := range conns {
		_ = c.Close()
	}
}

// --- Ping / Health Check ---

func TestGet_PingEvictsUnhealthyConn(t *testing.T) {
	srv := newTestServer(t)

	var pingCount atomic.Int32

	cfg := Config{
		MinSize:       2,
		MaxSize:       5,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
		Ping: func(c net.Conn) error {
			if pingCount.Add(1) == 1 {
				return fmt.Errorf("simulated ping failure")
			}
			return nil
		},
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	c, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = c.Close()

	if got := p.Stats().PingFailed(); got != 1 {
		t.Fatalf("PingFailed: got %d, want 1", got)
	}
}

// --- Idle Timeout ---

func TestGet_IdleTimeoutEvictsStaleConn(t *testing.T) {
	srv := newTestServer(t)

	cfg := Config{
		MinSize:       1,
		MaxSize:       5,
		Increment:     1,
		IdleTimeout:   50 * time.Millisecond,
		EvictInterval: -1,
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	ctx := context.Background()
	c, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = c.Close()

	// Wait for the connection to become idle.
	time.Sleep(100 * time.Millisecond)

	c2, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Get after idle: %v", err)
	}
	_ = c2.Close()

	if got := p.Stats().IdleClosed(); got < 1 {
		t.Fatalf("IdleClosed: got %d, want >= 1", got)
	}
}

// --- Max Lifetime ---

func TestGet_MaxLifetimeEvictsOldConn(t *testing.T) {
	srv := newTestServer(t)

	cfg := Config{
		MinSize:       1,
		MaxSize:       5,
		Increment:     1,
		IdleTimeout:   time.Minute,
		MaxLifetime:   50 * time.Millisecond,
		EvictInterval: -1,
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	ctx := context.Background()
	c, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = c.Close()

	// Wait for the connection to exceed max lifetime.
	time.Sleep(100 * time.Millisecond)

	c2, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Get after lifetime: %v", err)
	}
	_ = c2.Close()

	if got := p.Stats().LifetimeClosed(); got < 1 {
		t.Fatalf("LifetimeClosed: got %d, want >= 1", got)
	}
}

// --- Background Evictor ---

func TestEvictor_CleansStaleConnections(t *testing.T) {
	srv := newTestServer(t)

	cfg := Config{
		MinSize:       2,
		MaxSize:       5,
		Increment:     1,
		IdleTimeout:   50 * time.Millisecond,
		EvictInterval: 30 * time.Millisecond,
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	// Wait for connections to become idle and evictor to run.
	time.Sleep(200 * time.Millisecond)

	if got := p.Stats().IdleClosed(); got < 1 {
		t.Fatalf("IdleClosed: got %d, want >= 1", got)
	}
}

func TestEvictor_StopsOnPoolStop(t *testing.T) {
	srv := newTestServer(t)

	cfg := Config{
		MinSize:       1,
		MaxSize:       5,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: 10 * time.Millisecond,
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Stopping should not hang (evictor goroutine exits cleanly).
	done := make(chan struct{})
	go func() {
		_ = p.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() timed out — evictor may be stuck")
	}
}

// --- MarkUnusable ---

func TestMarkUnusable(t *testing.T) {
	srv := newTestServer(t)

	cfg := Config{
		MinSize:       1,
		MaxSize:       5,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	c, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	sizeBefore := p.Stats().Size()
	p.MarkUnusable(c)
	_ = c.Close()

	sizeAfter := p.Stats().Size()
	if sizeAfter >= sizeBefore {
		t.Fatalf("Size after MarkUnusable+Close: got %d, want < %d", sizeAfter, sizeBefore)
	}
}

// --- Concurrency ---

func TestConcurrency(t *testing.T) {
	tests := []struct {
		desc     string
		workers  int
		reqCount int
	}{
		{"10 workers", 10, 10},
		{"30 workers", 30, 50},
		{"100 workers", 100, 100},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			srv := newTestServer(t)

			cfg := Config{
				MinSize:       1,
				MaxSize:       5,
				Increment:     2,
				IdleTimeout:   time.Second,
				EvictInterval: -1,
			}
			p, err := New(cfg, testFactory(t, srv))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer func() { _ = p.Stop() }()

			ctx := context.Background()
			var successCount atomic.Int64
			var wg sync.WaitGroup
			wg.Add(tc.workers)

			for range tc.workers {
				go func() {
					defer wg.Done()
					for range tc.reqCount {
						c, err := p.Get(ctx)
						if err != nil {
							continue
						}
						successCount.Add(1)
						time.Sleep(time.Millisecond)
						_ = c.Close()
					}
				}()
			}
			wg.Wait()

			stats := p.Stats()
			wantReq := tc.workers * tc.reqCount
			if stats.Request() != wantReq {
				t.Fatalf("Request: got %d, want %d", stats.Request(), wantReq)
			}
			if stats.Success() != int(successCount.Load()) {
				t.Fatalf("Success: got %d, want %d", stats.Success(), successCount.Load())
			}
			if stats.Active() > cfg.MaxSize {
				t.Fatalf("Active=%d exceeds MaxSize=%d", stats.Active(), cfg.MaxSize)
			}
		})
	}
}

// --- Name ---

func TestName(t *testing.T) {
	p1, err := New(Config{MaxSize: 5, Increment: 1, EvictInterval: -1}, nilFactory)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p1.Name() == "" {
		t.Fatal("expected non-empty default name")
	}

	p2, err := New(Config{MaxSize: 5, Increment: 1, EvictInterval: -1}, nilFactory, WithName("my-pool"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p2.Name() != "my-pool" {
		t.Fatalf("Name: got %q, want %q", p2.Name(), "my-pool")
	}
}

// --- Stats ---

func TestStats(t *testing.T) {
	srv := newTestServer(t)

	cfg := Config{
		MinSize:       3,
		MaxSize:       5,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	stats := p.Stats()
	if stats.Available() != 3 {
		t.Fatalf("Available: got %d, want 3", stats.Available())
	}
	if stats.Size() != 3 {
		t.Fatalf("Size: got %d, want 3", stats.Size())
	}

	c, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	stats = p.Stats()
	if stats.Active() != 1 {
		t.Fatalf("Active: got %d, want 1", stats.Active())
	}
	if stats.Request() != 1 {
		t.Fatalf("Request: got %d, want 1", stats.Request())
	}
	if stats.Success() != 1 {
		t.Fatalf("Success: got %d, want 1", stats.Success())
	}

	_ = c.Close()
}

// --- Wait metrics ---

func TestStats_WaitMetrics(t *testing.T) {
	srv := newTestServer(t)

	cfg := Config{
		MinSize:       1,
		MaxSize:       1,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	c, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Return the connection after a delay so the waiter has to wait.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = c.Close()
	}()

	c2, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Get (wait): %v", err)
	}
	_ = c2.Close()

	stats := p.Stats()
	if stats.WaitCount() < 1 {
		t.Fatalf("WaitCount: got %d, want >= 1", stats.WaitCount())
	}
	if stats.WaitTime() < 30*time.Millisecond {
		t.Fatalf("WaitTime: got %v, want >= 30ms", stats.WaitTime())
	}
}

// --- Stop ---

func TestStop(t *testing.T) {
	srv := newTestServer(t)

	cfg := Config{
		MinSize:       3,
		MaxSize:       5,
		Increment:     1,
		IdleTimeout:   time.Minute,
		EvictInterval: -1,
	}
	p, err := New(cfg, testFactory(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Double stop should not panic or error.
	if err := p.Stop(); err != nil {
		t.Fatalf("double Stop: %v", err)
	}

	_, err = p.Get(context.Background())
	if err != ErrClosed {
		t.Fatalf("Get after Stop: got %v, want ErrClosed", err)
	}
}
