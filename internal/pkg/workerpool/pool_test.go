package workerpool

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedAndRecoversPanics(t *testing.T) {
	p := New(3)
	var cur, peak, done, panics atomic.Int32
	p.OnPanic = func(any, []byte) { panics.Add(1) }
	for i := 0; i < 20; i++ {
		i := i
		if err := p.Go(context.Background(), func() {
			n := cur.Add(1)
			for {
				pk := peak.Load()
				if n <= pk || peak.CompareAndSwap(pk, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			cur.Add(-1)
			if i%7 == 0 {
				panic("boom")
			}
			done.Add(1)
		}); err != nil {
			t.Fatal(err)
		}
	}
	p.Wait()
	if peak.Load() > 3 {
		t.Fatalf("peak %d > size 3", peak.Load())
	}
	if done.Load()+panics.Load() != 20 || panics.Load() != 3 {
		t.Fatalf("done=%d panics=%d", done.Load(), panics.Load())
	}
	if p.Running() != 0 {
		t.Fatalf("running = %d after Wait", p.Running())
	}
}

func TestGoRespectsContext(t *testing.T) {
	p := New(1)
	block := make(chan struct{})
	p.Go(context.Background(), func() { <-block })
	if p.TryGo(func() {}) {
		t.Fatal("TryGo succeeded on a full pool")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Go(ctx, func() {}); err == nil {
		t.Fatal("Go on a full pool ignored the context")
	}
	close(block)
	p.Wait()
}
