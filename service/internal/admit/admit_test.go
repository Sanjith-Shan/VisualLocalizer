package admit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestQueueFull(t *testing.T) {
	p := New(Config{Workers: 1, MaxQueue: 2})
	defer p.Close()
	block := make(chan struct{})
	var wg sync.WaitGroup
	started := make(chan struct{})
	wg.Add(1)
	go func() { defer wg.Done(); p.Do(context.Background(), func() { close(started); <-block }) }()
	<-started
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); p.Do(context.Background(), func() {}) }()
	}
	for p.QueueLen() < 2 {
		time.Sleep(time.Millisecond)
	}
	_, err := p.Do(context.Background(), func() { t.Error("ran a rejected job") })
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("got %v, want queue full", err)
	}
	close(block)
	wg.Wait()
}

func TestWouldMissAndExpired(t *testing.T) {
	p := New(Config{Workers: 1, MaxQueue: 10, InitSvc: 50 * time.Millisecond})
	defer p.Close()
	hold, held := make(chan struct{}), make(chan struct{})
	go p.Do(context.Background(), func() { close(held); <-hold })
	<-held
	defer close(hold)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	// Busy worker, 10 ms budget, 50 ms estimated service: rejected before queueing.
	if _, err := p.Do(ctx, func() {}); !errors.Is(err, ErrWouldMiss) {
		t.Fatalf("got %v, want would-miss", err)
	}
	var rej *Rejection
	_, err := p.Do(ctx, func() {})
	if !errors.As(err, &rej) || rej.RetryAfter < time.Second {
		t.Fatalf("rejection without retry hint: %v", err)
	}

	// With deadline shedding off, the job queues behind a blocker and expires there.
	q := New(Config{Workers: 1, MaxQueue: 10, NoDeadlineShed: true})
	defer q.Close()
	block := make(chan struct{})
	started := make(chan struct{})
	go q.Do(context.Background(), func() { close(started); <-block })
	<-started
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	go func() { time.Sleep(60 * time.Millisecond); close(block) }()
	if _, err := q.Do(ctx2, func() { t.Error("expired job ran") }); !errors.Is(err, ErrExpired) {
		t.Fatalf("got %v, want expired", err)
	}
}

func TestEstimateWait(t *testing.T) {
	p := New(Config{Workers: 2, MaxQueue: 10, InitSvc: 10 * time.Millisecond})
	defer p.Close()
	if w := p.EstimateWait(); w != 0 {
		t.Fatalf("idle pool estimates %v", w)
	}
	st, err := p.Do(context.Background(), func() { time.Sleep(5 * time.Millisecond) })
	if err != nil || st.Run < 5*time.Millisecond {
		t.Fatalf("%v %v", st, err)
	}
}

// An idle pool must admit even when its estimate says the job cannot make it, or a stale
// estimate rejects everything forever (no job runs, so it never updates).
func TestIdleAlwaysAdmits(t *testing.T) {
	p := New(Config{Workers: 2, MaxQueue: 10, InitSvc: 10 * time.Second})
	defer p.Close()
	for i := range 20 {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := p.Do(ctx, func() { time.Sleep(time.Millisecond) })
		cancel()
		if err != nil {
			t.Fatalf("request %d on an idle pool rejected: %v (estimate %v)", i, err, p.ServiceEstimate())
		}
	}
	if p.ServiceEstimate() > 2*time.Second {
		t.Errorf("estimate did not recover: %v", p.ServiceEstimate())
	}
}
