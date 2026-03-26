package connpool

import (
	"sync/atomic"
	"time"
)

type counter interface {
	inc() int
	dec() int
	add(n int64) int
	reset() int
	val() int
}

type availabler interface {
	available() int
}

type count struct {
	v int64
}

func newCounter() counter {
	return &count{}
}

func (c *count) inc() int {
	return int(atomic.AddInt64(&c.v, 1))
}

func (c *count) dec() int {
	return int(atomic.AddInt64(&c.v, -1))
}

func (c *count) add(n int64) int {
	return int(atomic.AddInt64(&c.v, n))
}

func (c *count) val() int {
	return int(atomic.LoadInt64(&c.v))
}

func (c *count) reset() int {
	return int(atomic.SwapInt64(&c.v, 0))
}

type stats struct {
	a              availabler
	size           counter
	request        counter
	success        counter
	idleClosed     counter
	lifetimeClosed counter
	pingFailed     counter
	waitCount      counter
	waitTimeNanos  counter // cumulative nanoseconds
}

func newStats(a availabler) *stats {
	return &stats{
		a:              a,
		size:           newCounter(),
		request:        newCounter(),
		success:        newCounter(),
		idleClosed:     newCounter(),
		lifetimeClosed: newCounter(),
		pingFailed:     newCounter(),
		waitCount:      newCounter(),
		waitTimeNanos:  newCounter(),
	}
}

func (s *stats) reset() {
	s.size.reset()
	s.success.reset()
	s.request.reset()
	s.idleClosed.reset()
	s.lifetimeClosed.reset()
	s.pingFailed.reset()
	s.waitCount.reset()
	s.waitTimeNanos.reset()
}

func (s *stats) snapshot() Stats {
	return &statsSnapshot{
		available:      s.a.available(),
		size:           s.size.val(),
		request:        s.request.val(),
		success:        s.success.val(),
		idleClosed:     s.idleClosed.val(),
		lifetimeClosed: s.lifetimeClosed.val(),
		pingFailed:     s.pingFailed.val(),
		waitCount:      s.waitCount.val(),
		waitTime:       time.Duration(s.waitTimeNanos.val()),
	}
}

type statsSnapshot struct {
	available      int
	size           int
	request        int
	success        int
	idleClosed     int
	lifetimeClosed int
	pingFailed     int
	waitCount      int
	waitTime       time.Duration
}

func (s *statsSnapshot) Available() int          { return s.available }
func (s *statsSnapshot) Size() int               { return s.size }
func (s *statsSnapshot) Request() int            { return s.request }
func (s *statsSnapshot) Success() int            { return s.success }
func (s *statsSnapshot) Active() int             { return s.size - s.available }
func (s *statsSnapshot) IdleClosed() int         { return s.idleClosed }
func (s *statsSnapshot) LifetimeClosed() int     { return s.lifetimeClosed }
func (s *statsSnapshot) PingFailed() int         { return s.pingFailed }
func (s *statsSnapshot) WaitCount() int          { return s.waitCount }
func (s *statsSnapshot) WaitTime() time.Duration { return s.waitTime }
