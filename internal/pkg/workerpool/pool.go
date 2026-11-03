// Package workerpool runs functions on a bounded number of goroutines.
//
// Video decoding and AI calls are memory- and quota-heavy; the pool caps how
// many run at once so a burst of uploads cannot exhaust memory.
package workerpool

import (
	"context"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

type Pool struct {
	sem     chan struct{}
	wg      sync.WaitGroup
	running atomic.Int32
	// OnPanic is called with the recovered value and stack when a function
	// panics. The pool keeps running either way.
	OnPanic func(v any, stack []byte)
}

func New(size int) *Pool {
	if size < 1 {
		size = 1
	}
	return &Pool{sem: make(chan struct{}, size)}
}

func (p *Pool) Size() int    { return cap(p.sem) }
func (p *Pool) Running() int { return int(p.running.Load()) }

// Go runs fn once a slot is free, or returns ctx.Err() if ctx ends first.
func (p *Pool) Go(ctx context.Context, fn func()) error {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	p.start(fn)
	return nil
}

// TryGo runs fn only if a slot is free right now.
func (p *Pool) TryGo(fn func()) bool {
	select {
	case p.sem <- struct{}{}:
		p.start(fn)
		return true
	default:
		return false
	}
}

func (p *Pool) start(fn func()) {
	p.wg.Add(1)
	p.running.Add(1)
	go func() {
		defer func() {
			if v := recover(); v != nil && p.OnPanic != nil {
				p.OnPanic(v, debug.Stack())
			}
			p.running.Add(-1)
			<-p.sem
			p.wg.Done()
		}()
		fn()
	}()
}

// Wait blocks until every started function has returned.
func (p *Pool) Wait() { p.wg.Wait() }
