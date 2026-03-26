package connpool

import (
	"sync/atomic"
	"time"
)

type availabler interface {
	available() int
}

type stats struct {
	a              availabler
	size           atomic.Int64
	request        atomic.Int64
	success        atomic.Int64
	idleClosed     atomic.Int64
	lifetimeClosed atomic.Int64
	pingFailed     atomic.Int64
	waitCount      atomic.Int64
	waitTimeNanos  atomic.Int64
}

func newStats(a availabler) *stats {
	return &stats{a: a}
}

func (s *stats) reset() {
	s.size.Store(0)
	s.success.Store(0)
	s.request.Store(0)
	s.idleClosed.Store(0)
	s.lifetimeClosed.Store(0)
	s.pingFailed.Store(0)
	s.waitCount.Store(0)
	s.waitTimeNanos.Store(0)
}

func (s *stats) snapshot() Stats {
	return &statsSnapshot{
		available:      s.a.available(),
		size:           int(s.size.Load()),
		request:        int(s.request.Load()),
		success:        int(s.success.Load()),
		idleClosed:     int(s.idleClosed.Load()),
		lifetimeClosed: int(s.lifetimeClosed.Load()),
		pingFailed:     int(s.pingFailed.Load()),
		waitCount:      int(s.waitCount.Load()),
		waitTime:       time.Duration(s.waitTimeNanos.Load()),
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
