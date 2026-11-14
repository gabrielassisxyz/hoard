package fetch

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// engine is the scheduler. One goroutine runs loop and owns every host's queue, pause and
// limiter, so the decisions need no locks; the requests themselves run in goroutines of
// their own and report back through results.
//
// Requests are started only when a worker slot is free, the host is not paused and its
// limiter has a token. Deciding that before starting, instead of letting a worker block on
// the limiter, is what keeps one slow or refusing host from holding every worker while
// other hosts wait.
type engine struct {
	cfg       RunConfig
	client    *http.Client
	hosts     []*hostQueue
	hostByKey map[string]*hostQueue
	cursor    int // where the next scan for a ready host starts, so hosts take turns

	inFlight  int
	remaining int // jobs not yet settled, queued or in flight
	stopping  bool
	results   chan attemptResult
	requests  sync.WaitGroup
}

func newEngine(cfg RunConfig) *engine {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = cfg.Workers
	return &engine{
		cfg:       cfg,
		client:    &http.Client{Timeout: cfg.Timeout, Transport: transport},
		hostByKey: make(map[string]*hostQueue),
		// Never full: each request in flight sends exactly once, and there are at most
		// Workers of them.
		results: make(chan attemptResult, cfg.Workers),
	}
}

func (e *engine) loop(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	interrupted := ctx.Done()
	var failure error
	for e.remaining > 0 && (!e.stopping || e.inFlight > 0) {
		if runCtx.Err() != nil {
			e.stopping = true
		}
		var wakeAt time.Time
		if !e.stopping {
			wakeAt = e.startReadyJobs(runCtx)
		}
		var timer *time.Timer
		var wake <-chan time.Time
		if !wakeAt.IsZero() {
			timer = time.NewTimer(time.Until(wakeAt))
			wake = timer.C
		}
		select {
		case result := <-e.results:
			if err := e.settle(result); err != nil && failure == nil {
				// The ledger or the disk gave out: asking the network for more would only
				// pile up pages that cannot be recorded.
				failure, e.stopping = err, true
				cancel()
			}
		case <-wake:
		case <-interrupted:
			e.stopping = true
			interrupted = nil
		}
		if timer != nil {
			timer.Stop()
		}
	}
	e.requests.Wait()
	return failure
}

// startReadyJobs starts everything that can start now and returns the moment something
// next may become startable, or the zero time when only a finished request can change
// anything.
func (e *engine) startReadyJobs(ctx context.Context) time.Time {
	for e.inFlight < e.cfg.Workers && e.startOneReadyJob(ctx) {
	}
	if e.inFlight >= e.cfg.Workers {
		return time.Time{}
	}
	return e.earliestReadiness()
}

func (e *engine) startOneReadyJob(ctx context.Context) bool {
	now := time.Now()
	for i := range e.hosts {
		position := (e.cursor + i) % len(e.hosts)
		host := e.hosts[position]
		if len(host.waiting) == 0 || now.Before(host.pausedUntil) || !host.limiter.AllowN(now, 1) {
			continue
		}
		e.cursor = position + 1
		e.start(ctx, host.takeNext())
		return true
	}
	return false
}

func (e *engine) earliestReadiness() time.Time {
	now := time.Now()
	var earliest time.Time
	for _, host := range e.hosts {
		if len(host.waiting) == 0 {
			continue
		}
		if at := host.readyAt(now); earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	return earliest
}

func (e *engine) start(ctx context.Context, job *pageJob) {
	e.inFlight++
	e.requests.Add(1)
	go func() {
		defer e.requests.Done()
		e.results <- e.attempt(ctx, job)
	}()
}

// settle applies what an attempt learned: the ledger row, the host's pause, and whether
// the address goes back in line. A non-nil error means the store failed.
func (e *engine) settle(result attemptResult) error {
	e.inFlight--
	switch result.kind {
	case attemptCancelled:
		return nil
	case attemptBroken:
		return result.storeErr
	}
	job := result.job
	job.attempts++
	if result.outcome.HTTPStatus != 0 && result.kind != attemptRefused {
		job.host.refusals = 0
	}
	switch result.kind {
	case attemptStored:
		e.remaining--
		return e.cfg.Store.MarkOK(job.address, result.outcome)
	case attemptFinal:
		e.remaining--
		return e.cfg.Store.MarkFailed(job.address, result.outcome)
	case attemptRefused:
		e.pauseHost(job.host, result)
		return e.tryAgainOrGiveUp(job, result, true)
	default:
		return e.tryAgainOrGiveUp(job, result, false)
	}
}

// pauseHost rests a host that refused. The base is what the host asked for, or the default
// when it named nothing, and it doubles with each refusal in a row.
func (e *engine) pauseHost(host *hostQueue, result attemptResult) {
	base := e.cfg.RefusalPause
	if result.hasRetryAfter {
		base = result.retryAfter
	}
	host.refusals++
	until := time.Now().Add(refusalPause(base, host.refusals))
	if until.After(host.pausedUntil) {
		host.pausedUntil = until
	}
}

func (e *engine) tryAgainOrGiveUp(job *pageJob, result attemptResult, atFront bool) error {
	if job.attempts >= maxAttemptsPerRun {
		e.remaining--
		return e.cfg.Store.MarkFailed(job.address, result.outcome)
	}
	if err := e.cfg.Store.NoteAttempt(job.address, result.outcome); err != nil {
		return err
	}
	if !e.stopping {
		job.host.requeue(job, atFront)
	}
	return nil
}
