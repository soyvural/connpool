package connpool

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestAtomicInt64_ConcurrentAddLoad(t *testing.T) {
	var c atomic.Int64
	concur := 1000
	loop := 1000
	want := int64(concur * loop)

	var wg sync.WaitGroup
	wg.Add(concur)
	for range concur {
		go func() {
			defer wg.Done()
			for range loop {
				c.Add(1)
			}
		}()
	}
	wg.Wait()

	if c.Load() != want {
		t.Fatalf("got %d, want %d", c.Load(), want)
	}
}
