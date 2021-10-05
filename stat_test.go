package connpool

import (
	"sync"
	"testing"
)

func TestCounterInc(t *testing.T) {
	c := newCounter()
	wg := sync.WaitGroup{}
	concur := 1000
	loop := 1000
	want := concur * loop

	wg.Add(concur)
	for i := 0; i < concur; i++ {
		go func() {
			defer wg.Done()
			for i := 0; i < loop; i++ {
				c.inc()
			}
		}()
	}
	wg.Wait()

	if c.val() != want {
		t.Fatalf("inc error, -want:%d, +got:%d", want, c.val())
	}
}

func TestCounterDec(t *testing.T) {
	c := newCounter()
	wg := sync.WaitGroup{}
	concur := 1000
	loop := 1000
	c.(*count).v = int64(concur * loop)

	wg.Add(concur)
	for i := 0; i < concur; i++ {
		go func() {
			defer wg.Done()
			for i := 0; i < loop; i++ {
				c.dec()
			}
		}()
	}
	wg.Wait()

	if c.val() != 0 {
		t.Fatalf("dec error, -want:%d, +got:%d", 0, c.val())
	}
}

func TestCounterReset(t *testing.T) {
	c := newCounter()
	wg := sync.WaitGroup{}
	conCount := 1000
	loop := 1000

	wg.Add(conCount)
	for i := 0; i < conCount; i++ {
		go func() {
			defer wg.Done()
			for i := 0; i < loop; i++ {
				c.inc()
			}
		}()
	}
	wg.Wait()

	c.reset()
	if c.val() != 0 {
		t.Fatalf("reset error, -want:%d, +got:%d", 0, c.val())
	}
}
