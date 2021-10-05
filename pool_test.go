package connpool

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	cfg = Config{
		MinSize:     1,
		MaxSize:     10,
		Increment:   1,
		IdleTimeout: 10 * time.Minute,
	}

	fakeFactory = func() (net.Conn, error) {
		return nil, nil
	}

	fakeFactorySrv = func(s *server) Factory {
		return func() (net.Conn, error) {
			return net.Dial(s.l.Addr().Network(), s.l.Addr().String())
		}
	}
)

type server struct {
	t         *testing.T
	host      string
	port      uint
	l         net.Listener
	connSize  counter
	terminate chan struct{}
}

func newTestSrv() *server {
	return &server{port: 0, terminate: make(chan struct{}, 1), connSize: newCounter()}
}

func (s *server) start() error {
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return err
	}
	s.l = l
	go s.listen()
	return nil
}

func (s *server) listen() {
	for {
		select {
		case <-s.terminate:
			s.l.Close()
			return
		default:
			_, err := s.l.Accept()
			if err != nil {
				log.Fatal(err)
			}
		}
	}
}

func (s *server) stop() {
	s.terminate <- struct{}{}

}

func TestNew(t *testing.T) {
	tests := []struct {
		desc    string
		cfg     Config
		name    string
		wantErr bool
	}{
		{
			desc:    "empty pool",
			cfg:     Config{},
			name:    "empty pool",
			wantErr: true,
		},
		{
			desc: "min is grater than max",
			cfg: Config{
				MinSize:     2,
				MaxSize:     1,
				Increment:   1,
				IdleTimeout: time.Minute,
			},
			wantErr: true,
		},
		{
			desc: "invalid increment",
			cfg: Config{
				MinSize:     6,
				MaxSize:     10,
				Increment:   5,
				IdleTimeout: time.Minute,
			},
			wantErr: true,
		},
		{
			desc: "with conn factory",
			cfg: Config{
				MinSize:     5,
				MaxSize:     30,
				Increment:   1,
				IdleTimeout: time.Minute,
			},
			wantErr: false,
		},
		{
			desc: "with address",
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
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			_, err := New(tc.cfg, fakeFactory)
			if (err != nil) != tc.wantErr {
				t.Fatalf("New error, -wantErr: %v, +gotErr: %v, err: %v", tc.wantErr, err != nil, err)
			}
		})
	}
}

func TestPool_Get(t *testing.T) {
	tests := []struct {
		desc      string
		cfg       Config
		demanding int
		wantErr   bool
	}{
		{
			desc:      "get",
			demanding: 10,
			cfg: Config{
				MinSize:     1,
				MaxSize:     10,
				Increment:   1,
				IdleTimeout: 100 * time.Minute,
			},
		},
		{
			desc:      "with increment 2",
			demanding: 10,
			cfg: Config{
				MinSize:     5,
				MaxSize:     10,
				Increment:   2,
				IdleTimeout: 100 * time.Millisecond,
			},
		},
		{
			desc:      "demanding more than max",
			demanding: 11,
			cfg: Config{
				MinSize:     5,
				MaxSize:     10,
				Increment:   3,
				IdleTimeout: 100 * time.Millisecond,
			},
			wantErr: true,
		},
		{
			desc:      "demanding less than max",
			demanding: 6,
			cfg: Config{
				MinSize:     5,
				MaxSize:     10,
				Increment:   3,
				IdleTimeout: 100 * time.Millisecond,
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			srv := newTestSrv()
			if err := srv.start(); err != nil {
				log.Fatalf("Failed to start test server, err: %v.", err)
			}
			defer srv.stop()

			p, err := New(tc.cfg, fakeFactorySrv(srv))
			if err != nil {
				t.Fatalf("new error, err: %v", err)
			}
			defer p.Stop()

			wg := sync.WaitGroup{}
			for i := 0; i < tc.demanding; i++ {
				conn, err := p.Get()
				if err != nil {
					if tc.wantErr {
						continue
					}
					t.Fatalf("get error, err: %v", err)

				}

				wg.Add(1)
				go func() {
					defer wg.Done()
					time.Sleep(5 * time.Millisecond)
					conn.Close()
				}()
			}
			wg.Wait()
			if p.Stats().Active() > tc.cfg.MaxSize {
				t.Fatalf("conn management err, active: %d, maxSize: %d", p.Stats().Active(), tc.cfg.MaxSize)
			}
		})
	}
}

func TestPool_Start(t *testing.T) {
	tests := []struct {
		desc      string
		name      string
		autoStart bool
	}{
		{
			desc:      "start",
			name:      "test_pool",
			autoStart: false,
		},
		{
			desc:      "auto start",
			name:      "test_pool",
			autoStart: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			p, err := New(cfg, fakeFactory)
			if err != nil {
				t.Fatalf("new err, err: %v", err)
			}
			if tc.autoStart && atomic.LoadInt32(&p.(*pool).running) == 0 {
				t.Fatal("start err, it is not running though it is configured to auto start.")
			}
			if atomic.LoadInt32(&p.(*pool).running) == 0 {
				t.Fatal("start err, it is not running.")
			}
		})
	}
}

func TestPool_Stop(t *testing.T) {
	tests := []struct {
		desc string
		name string
	}{
		{
			desc: "start",
			name: "test_pool",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			p, err := New(cfg, fakeFactory)
			if err != nil {
				t.Fatalf("new err, err: %v", err)
			}
			_ = p.Stop()
			if atomic.LoadInt32(&p.(*pool).running) == 1 {
				t.Fatal("stop err, it is still running.")
			}
		})
	}
}

func TestPool_Name(t *testing.T) {
	tests := []struct {
		desc string
		name string
	}{
		{
			desc: " name",
			name: "test_pool",
		},
		{
			desc: "default name",
		},
	}

	for _, tc := range tests {
		tc := tc
		var opts []Option
		if tc.name != "" {
			opts = append(opts, WithName(tc.name))
		}
		p, err := New(cfg, fakeFactory, opts...)
		if err != nil {
			t.Fatalf("new err, err: %v", err)
		}

		if tc.name == "" && !strings.HasPrefix(p.Name(), defaultNamePrefix) {
			t.Fatalf("Name error, -want:%s*, +got:%s", defaultNamePrefix, p.Name())
		}
		if tc.name != "" && tc.name != p.Name() {
			t.Fatalf("Name error, -name: %v, +name: %v", tc.name, p.Name())
		}
	}
}

func TestConcurrency(t *testing.T) {
	tests := []struct {
		desc        string
		workersSize int
		reqCount    int
	}{
		{
			desc:        "with 10 consumers",
			workersSize: 10,
			reqCount:    10,
		},
		{
			desc:        "with 30 consumers",
			workersSize: 30,
			reqCount:    50,
		},
		{
			desc:        "with 100 consumers",
			workersSize: 100,
			reqCount:    250,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			srv := newTestSrv()
			if err := srv.start(); err != nil {
				log.Fatalf("Failed to start test server, err: %v.", err)
			}
			defer srv.stop()

			cfg := Config{
				MinSize:     1,
				MaxSize:     5,
				Increment:   3,
				IdleTimeout: time.Second,
			}
			p, err := New(cfg, fakeFactorySrv(srv))
			if err != nil {
				log.Fatalf("new error, err: %v.", err)
			}

			var successCount int64
			wg := sync.WaitGroup{}
			wg.Add(tc.workersSize)
			for i := 0; i < tc.workersSize; i++ {
				go func() {
					defer wg.Done()
					for i := 0; i < tc.reqCount; i++ {
						active, available := p.Stats().Active(), p.Stats().Available()
						if active+available > cfg.MaxSize {
							t.Fatalf("conn management error, active: %d, available: %d, maxConn: %d", active, available, cfg.MaxSize)
						}

						c, _ := p.Get()
						if c != nil {
							atomic.AddInt64(&successCount, 1)
							time.Sleep(20 * time.Millisecond)
							_ = c.Close()
						}
					}
				}()
			}
			wg.Wait()
			if p.Stats().Active() > cfg.MaxSize {
				t.Fatalf("active conn count failure, avtive: %d should not be larger than maxSize: %d", p.Stats().Active(), cfg.MaxSize)
			}

			wantReqCount := tc.workersSize * tc.reqCount
			if p.Stats().Request() != wantReqCount {
				t.Fatalf("request count failure, -wantedReqCount: %d, +gotReqCount: %d", wantReqCount, p.Stats().Request())
			}

			if p.Stats().Success() != int(successCount) {
				t.Fatalf("request count failurer, -wantedSuccessCount: %d, +gotSuccessCount: %d", successCount, p.Stats().Success())
			}

			if p.Stats().Available() != cfg.MaxSize {
				t.Fatalf("available conn count failure, -wantedAvailableCount: %d, +gotAvailableCount: %d", cfg.MaxSize, p.Stats().Available())
			}
		})
	}
}

func TestPool_MarkUnusable(t *testing.T) {
	p, _ := New(cfg, fakeFactory)

	c, _ := p.Get()
	defer c.Close()

	p.MarkUnusable(c)
	pc, ok := c.(*conn)
	if !ok {
		t.Fatalf("unexpected type %T", pc)
	}
	if !pc.isUnUsable() {
		t.Fatal("conn must be unusable")
	}
}
