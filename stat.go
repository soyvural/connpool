package connpool

import "sync/atomic"

type counter interface {
	inc() (newVal int)
	dec() (newVal int)
	reset() (newVal int)
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

func (c *count) inc() (new int) {
	return int(atomic.AddInt64(&c.v, 1))
}

func (c *count) dec() (new int) {
	return int(atomic.AddInt64(&c.v, -1))
}

func (c count) val() int {
	return int(atomic.LoadInt64(&c.v))
}

func (c *count) reset() (old int) {
	return int(atomic.SwapInt64(&c.v, 0))
}

type stats struct {
	a       availabler
	size    counter
	request counter
	success counter
}

func newStats(a availabler) *stats {
	return &stats{
		a:       a,
		size:    newCounter(),
		request: newCounter(),
		success: newCounter(),
	}
}

func (s *stats) reset() {
	s.size.reset()
	s.success.reset()
	s.request.reset()
}

func (s *stats) snapshot() Stats {
	return &statsSnapshot{
		available: s.a.available(),
		size:      s.size.val(),
		request:   s.request.val(),
		success:   s.success.val(),
	}
}

type statsSnapshot struct {
	available int
	size      int
	request   int
	success   int
}

func (s *statsSnapshot) Available() int {
	return s.available
}

func (s *statsSnapshot) Request() int {
	return s.request
}

func (s *statsSnapshot) Success() int {
	return s.success
}

func (s *statsSnapshot) Active() int {
	return s.size - s.available
}
