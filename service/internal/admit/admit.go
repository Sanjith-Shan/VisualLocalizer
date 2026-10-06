// Package admit runs CPU-bound jobs on a fixed pool of workers behind an admission
// gate.
//
// Each worker locks its OS thread, because a cgo call into the core blocks that thread
// for its whole duration and the pool size is the real concurrency limit. Jobs wait in
// a bounded FIFO queue. Admission rejects a job up front when the queue is full, or when
// the estimated queue wait plus service time would already overrun the job's deadline:
// a request that cannot finish in time is cheaper to refuse at once (503 with
// Retry-After) than to run late. A job whose deadline passes while it waits is dropped
// by the worker without running.
package admit

import (
	"context"
	"errors"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrQueueFull means the queue was at its limit.
	ErrQueueFull = errors.New("queue full")
	// ErrWouldMiss means the estimated wait plus service time exceeds the deadline.
	ErrWouldMiss = errors.New("deadline would be missed")
	// ErrExpired means the deadline passed while the job was queued.
	ErrExpired = errors.New("deadline expired in queue")
	// ErrClosed means the pool is shutting down.
	ErrClosed = errors.New("pool closed")
)

// Config sizes the pool.
type Config struct {
	Workers  int           // default runtime.NumCPU()
	MaxQueue int           // default 4 * Workers
	InitSvc  time.Duration // initial service-time estimate before any job finished, default 50ms
	// NoDeadlineShed turns off deadline-aware admission, leaving only the queue limit.
	// Used to show what shedding buys.
	NoDeadlineShed bool
}

// Rejection carries the reason and a retry hint.
type Rejection struct {
	Err        error
	RetryAfter time.Duration
	EstWait    time.Duration
}

func (r *Rejection) Error() string { return r.Err.Error() }
func (r *Rejection) Unwrap() error { return r.Err }

// Stats describe one admitted job.
type Stats struct {
	Queued time.Duration // time from Do to a worker picking it up
	Run    time.Duration // time inside fn
}

type job struct {
	ctx      context.Context
	fn       func()
	enqueued time.Time
	picked   time.Time
	ran      bool
	run      time.Duration
	done     chan struct{}
}

// Pool is a fixed worker pool with admission control.
type Pool struct {
	cfg      Config
	q        chan *job
	inflight atomic.Int64
	svcNs    atomic.Int64 // EWMA of fn run time
	devNs    atomic.Int64 // EWMA of |run time - mean|
	wg       sync.WaitGroup
	closeMu  sync.RWMutex
	closed   bool
}

func New(cfg Config) *Pool {
	if cfg.Workers <= 0 {
		cfg.Workers = runtime.NumCPU()
	}
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = 4 * cfg.Workers
	}
	if cfg.InitSvc <= 0 {
		cfg.InitSvc = 50 * time.Millisecond
	}
	p := &Pool{cfg: cfg, q: make(chan *job, cfg.MaxQueue)}
	p.svcNs.Store(int64(cfg.InitSvc))
	for range cfg.Workers {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

func (p *Pool) worker() {
	defer p.wg.Done()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for j := range p.q {
		j.picked = time.Now()
		if j.ctx.Err() == nil {
			p.inflight.Add(1)
			j.fn()
			p.inflight.Add(-1)
			j.ran = true
			j.run = time.Since(j.picked)
			p.observe(j.run)
		}
		close(j.done)
	}
}

func (p *Pool) observe(d time.Duration) {
	const alpha = 0.1
	ewma := func(v *atomic.Int64, x float64) {
		for {
			old := v.Load()
			if v.CompareAndSwap(old, int64(alpha*x+(1-alpha)*float64(old))) {
				return
			}
		}
	}
	mean := p.svcNs.Load()
	ewma(&p.svcNs, float64(d))
	ewma(&p.devNs, math.Abs(float64(d)-float64(mean)))
}

// ownBudget is the service time a newly admitted job must still have room for after
// its queue wait: the mean plus two mean deviations, because run times vary (frame
// content, and on hybrid CPUs whether the worker lands on a fast or slow core) and the
// admission promise is about the job's own tail, not the average job.
func (p *Pool) ownBudget() time.Duration {
	return time.Duration(p.svcNs.Load() + 2*p.devNs.Load())
}

// Workers is the pool size.
func (p *Pool) Workers() int { return p.cfg.Workers }

// QueueLen is the number of jobs waiting.
func (p *Pool) QueueLen() int { return len(p.q) }

// Inflight is the number of jobs running.
func (p *Pool) Inflight() int { return int(p.inflight.Load()) }

// ServiceEstimate is the current EWMA of run time.
func (p *Pool) ServiceEstimate() time.Duration { return time.Duration(p.svcNs.Load()) }

// EstimateWait predicts the queue wait for a job admitted now.
func (p *Pool) EstimateWait() time.Duration {
	ahead := len(p.q) + int(p.inflight.Load()) - p.cfg.Workers + 1
	if ahead <= 0 {
		return 0
	}
	// Jobs ahead drain at Workers per service time.
	waves := math.Ceil(float64(ahead) / float64(p.cfg.Workers))
	return time.Duration(waves * float64(p.svcNs.Load()))
}

// Do runs fn on a worker, or rejects it. A *Rejection error means fn never ran.
// The caller's ctx deadline is the job's deadline.
func (p *Pool) Do(ctx context.Context, fn func()) (Stats, error) {
	p.closeMu.RLock()
	if p.closed {
		p.closeMu.RUnlock()
		return Stats{}, &Rejection{Err: ErrClosed, RetryAfter: time.Second}
	}
	svc := p.ownBudget()
	wait := p.EstimateWait()
	// When a worker is free the job runs at once, so it is always admitted. This also
	// keeps the estimate alive: the EWMA only learns from jobs that run, and an estimate
	// inflated by a burst would otherwise reject everything forever (results/service/
	// sweep_shed_on_spiral.json, docs/BUG_LOG.md).
	idle := wait == 0 && len(p.q) == 0
	if dl, ok := ctx.Deadline(); ok && !p.cfg.NoDeadlineShed && !idle {
		if time.Until(dl) < wait+svc {
			p.closeMu.RUnlock()
			return Stats{}, &Rejection{Err: ErrWouldMiss, RetryAfter: retryAfter(wait), EstWait: wait}
		}
	}
	j := &job{ctx: ctx, fn: fn, enqueued: time.Now(), done: make(chan struct{})}
	select {
	case p.q <- j:
	default:
		p.closeMu.RUnlock()
		return Stats{}, &Rejection{Err: ErrQueueFull, RetryAfter: retryAfter(wait), EstWait: wait}
	}
	p.closeMu.RUnlock()
	// The job is owned by the queue now: wait for a worker even if ctx ends, because
	// fn may already be running and touching the caller's memory.
	<-j.done
	st := Stats{Queued: j.picked.Sub(j.enqueued)}
	if !j.ran {
		return st, &Rejection{Err: ErrExpired, RetryAfter: retryAfter(p.EstimateWait())}
	}
	st.Run = j.run
	return st, nil
}

func retryAfter(wait time.Duration) time.Duration {
	if wait < time.Second {
		return time.Second
	}
	return wait.Round(time.Second)
}

// Close stops admitting, lets queued jobs finish, and waits for the workers.
func (p *Pool) Close() {
	p.closeMu.Lock()
	if !p.closed {
		p.closed = true
		close(p.q)
	}
	p.closeMu.Unlock()
	p.wg.Wait()
}
