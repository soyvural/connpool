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

func newCounter() counter {
	return &count{}
}

type stats struct {
	a       availabler
	size    counter
	request counter
	success counter
}

func (s *stats) Available() int {
	return s.a.available()
}

func (s *stats) Request() int {
	return s.request.val()
}

func (s *stats) Success() int {
	return s.success.val()
}

func (s *stats) Active() int {
	return s.size.val() - s.a.available()
}

func (s *stats) reset() {
	s.size.reset()
	s.success.reset()
	s.request.reset()
}

func newStats(a availabler) *stats {
	return &stats{
		a:       a,
		size:    newCounter(),
		request: newCounter(),
		success: newCounter(),
	}
}
