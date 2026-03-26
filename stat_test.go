package connpool

import (
	"sync"
	"testing"
)

func TestCounterInc(t *testing.T) {
	c := newCounter()
	concur := 1000
	loop := 1000
	want := concur * loop

	var wg sync.WaitGroup
	wg.Add(concur)
	for range concur {
		go func() {
			defer wg.Done()
			for range loop {
				c.inc()
			}
		}()
	}
	wg.Wait()

	if c.val() != want {
		t.Fatalf("inc: got %d, want %d", c.val(), want)
	}
}

func TestCounterDec(t *testing.T) {
	c := newCounter()
	concur := 1000
	loop := 1000
	c.(*count).v = int64(concur * loop)

	var wg sync.WaitGroup
	wg.Add(concur)
	for range concur {
		go func() {
			defer wg.Done()
			for range loop {
				c.dec()
			}
		}()
	}
	wg.Wait()

	if c.val() != 0 {
		t.Fatalf("dec: got %d, want 0", c.val())
	}
}

func TestCounterReset(t *testing.T) {
	c := newCounter()
	for range 100 {
		c.inc()
	}
	c.reset()
	if c.val() != 0 {
		t.Fatalf("reset: got %d, want 0", c.val())
	}
}
