# connpool

[![CI](https://github.com/soyvural/connpool/actions/workflows/ci.yml/badge.svg)](https://github.com/soyvural/connpool/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/soyvural/connpool.svg)](https://pkg.go.dev/github.com/soyvural/connpool)
[![Go Report Card](https://goreportcard.com/badge/github.com/soyvural/connpool)](https://goreportcard.com/report/github.com/soyvural/connpool)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)

A production-grade TCP connection pool for Go. Designed for high-throughput, low-latency network workloads where connection reuse matters.

## Why This Pool?

Most Go projects that need TCP connection pooling either use archived/unmaintained libraries (like `fatih/pool`) or build their own. This pool fills the gap with the features that production systems actually need:

- **Health check on Get()** — stale/dead connections are detected before use
- **Idle timeout** — connections sitting unused are evicted automatically
- **Max lifetime with jitter** — prevents thundering herd reconnection storms
- **Background evictor** — proactively cleans stale connections (don't wait for Get())
- **Context-aware Get()** — respects deadlines and cancellation
- **Zero-alloc fast path** — channel-based pool with 0 allocs on Get/Put (~139ns/op)
- **Comprehensive metrics** — idle closed, lifetime closed, ping failures, wait time

## Install

```shell
go get github.com/soyvural/connpool
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "net"
    "time"

    "github.com/soyvural/connpool"
)

func main() {
    cfg := connpool.Config{
        MinSize:     5,
        MaxSize:     20,
        Increment:   2,
        IdleTimeout: 30 * time.Second,
        MaxLifetime: 5 * time.Minute,
        Ping: func(c net.Conn) error {
            // Quick health check: try a zero-byte read with short deadline.
            // Timeout = healthy (nothing to read). Any other error = dead.
            c.SetReadDeadline(time.Now().Add(time.Millisecond))
            buf := make([]byte, 1)
            if _, err := c.Read(buf); err != nil {
                if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
                    c.SetReadDeadline(time.Time{})
                    return nil // timeout = connection is alive
                }
                return err // connection is dead
            }
            c.SetReadDeadline(time.Time{})
            return nil
        },
    }

    factory := func() (net.Conn, error) {
        return net.DialTimeout("tcp", "localhost:9090", 10*time.Second)
    }

    p, err := connpool.New(cfg, factory, connpool.WithName("my-pool"))
    if err != nil {
        panic(err)
    }
    defer p.Stop()

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    conn, err := p.Get(ctx)
    if err != nil {
        panic(err)
    }
    defer conn.Close() // returns to pool (or destroys if marked unusable)

    // Use conn...
    fmt.Fprintf(conn, "hello\n")

    // If write fails, mark unusable before closing:
    // p.MarkUnusable(conn)

    // Check pool health:
    stats := p.Stats()
    fmt.Printf("Pool %s: size=%d active=%d available=%d idle_closed=%d lifetime_closed=%d ping_failed=%d\n",
        p.Name(), stats.Size(), stats.Active(), stats.Available(),
        stats.IdleClosed(), stats.LifetimeClosed(), stats.PingFailed())
}
```

## Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `MinSize` | `int` | 0 | Connections created on startup. Maintained by evictor. |
| `MaxSize` | `int` | required | Maximum total connections (idle + active). |
| `Increment` | `int` | required, >= 1 | How many connections to create when pool needs to grow. |
| `IdleTimeout` | `Duration` | 0 (disabled) | Close connections idle longer than this. |
| `MaxLifetime` | `Duration` | 0 (disabled) | Close connections older than this. 10% jitter applied automatically. |
| `Ping` | `func(net.Conn) error` | nil (disabled) | Health check called on Get() before returning connection. |
| `EvictInterval` | `Duration` | 30s | Background evictor frequency. Set -1 to disable. |

## How It Works

```
Get(ctx) flow:
  1. Try non-blocking channel read (fast path, ~139ns)
  2. Check health: lifetime -> idle timeout -> ping
  3. If unhealthy, discard and retry (up to 3 times)
  4. If no idle conn available, grow pool (up to MaxSize)
  5. If at MaxSize, block until conn returned or ctx cancelled

Put (via conn.Close()) flow:
  1. If marked unusable -> destroy, decrement size
  2. If pool stopped -> destroy
  3. Stamp lastUsed time, push to channel
  4. If channel full -> destroy (overflow)

Background evictor (every EvictInterval):
  1. Drain channel, health-check each connection
  2. Discard stale/expired connections
  3. Replenish to MinSize if needed
```

## Metrics

All metrics are available via `pool.Stats()`:

| Metric | Description |
|--------|-------------|
| `Size()` | Total connections (idle + active) |
| `Available()` | Idle connections in pool |
| `Active()` | Connections currently checked out |
| `Request()` | Total Get() calls |
| `Success()` | Successful Get() calls |
| `IdleClosed()` | Connections closed due to idle timeout |
| `LifetimeClosed()` | Connections closed due to max lifetime |
| `PingFailed()` | Connections discarded due to ping failure |
| `WaitCount()` | Get() calls that had to wait (pool at max) |
| `WaitTime()` | Cumulative time spent waiting |

## Benchmarks

```
goos: darwin
goarch: arm64
cpu: Apple M2 Pro
BenchmarkGetPut_Sequential-10    8,556,068    139.9 ns/op    0 B/op    0 allocs/op
BenchmarkGetPut_Parallel-10      5,075,728    245.1 ns/op    0 B/op    0 allocs/op
BenchmarkGetPut_WithPing-10        216,386   5571   ns/op   81 B/op    2 allocs/op
BenchmarkGetPut_Contended-10     2,405,068    478.7 ns/op    0 B/op    0 allocs/op
```

## Examples

Working examples are in the [`examples/`](examples/) directory:

| Example | Description |
|---------|-------------|
| [tcp-echo](examples/tcp-echo) | Basic pool usage with a TCP echo server |
| [redis-proxy](examples/redis-proxy) | Connection pooling with Ping health checks against Redis |
| [load-balancer](examples/load-balancer) | Round-robin load balancing across multiple backends using independent pools |

```shell
# TCP echo (start: ncat -l -k -p 9090 --sh-exec "cat")
go run ./examples/tcp-echo

# Redis proxy (start: docker run -d -p 6379:6379 redis:alpine)
go run ./examples/redis-proxy

# Load balancer (start two ncat echo servers on ports 9001 and 9002)
go run ./examples/load-balancer
```

## Design Decisions

**Channel-based pool over mutex+slice**: Channels give us natural blocking semantics for the wait-for-connection path, and the non-blocking `select`/`default` pattern makes the fast path extremely cheap (~139ns, 0 allocs).

**Max lifetime with jitter**: When all connections are created at the same time (startup), they'd all expire at the same time — causing a reconnection storm ("thundering herd"). Adding 10% random jitter spreads expiration across time.

**Health check order**: Lifetime check -> idle check -> ping. The cheapest checks (time comparisons) run first. The expensive check (network ping) only runs if the connection passed the time-based checks.

**Background evictor**: Without it, stale connections only get cleaned on Get(). If the pool is idle (no Get() calls), dead connections accumulate. The evictor proactively cleans them and maintains the minimum pool size.

## Ideal Connection Pool Properties

This pool was designed based on patterns from production-grade pools at scale:

| Property | Source | Status |
|----------|--------|--------|
| Idle timeout | pgx, go-redis, Vitess | Implemented |
| Max lifetime with jitter | pgx, Vitess | Implemented |
| Health check on borrow | go-redis, Apache Commons Pool | Implemented |
| Background evictor | pgx, Apache Commons Pool, Vitess | Implemented |
| Context-aware Get | Vitess, pgx | Implemented |
| Zero-alloc fast path | go-redis | Achieved |
| Comprehensive metrics | Vitess | Implemented |
| Min idle maintenance | pgx | Implemented (via evictor) |
| MarkUnusable | fatih/pool | Implemented |
