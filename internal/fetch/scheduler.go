package fetch

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// fetchScheduler is the scheduler. One goroutine runs loop and owns every host's queue and
// pause and the run's limiter, so the decisions need no locks; the requests themselves run
// in goroutines of their own and report back through results.
//
// Requests are started only when a worker slot is free, the host is not paused and the
// run's limiter has a token. Deciding that before starting, instead of letting a worker
// block on the limiter, is what keeps one slow or refusing host from holding every worker
// while other hosts wait.
type fetchScheduler struct {
	cfg    RunConfig
	client *http.Client
	// limiter spaces every request of the run, whatever host it goes to. A site that sends
	// each address on to another of its own names is one server answering twice per page, and
	// a limiter per name would ask it twice as fast as the rate that was set.
	limiter   *rate.Limiter
	hosts     []*hostQueue
	hostByKey map[string]*hostQueue
	cursor    int // where the next scan for a ready host starts, so hosts take turns

	inFlight  int
	remaining int // jobs not yet settled, queued or in flight
	stopping  bool
	results   chan attemptResult
	requests  sync.WaitGroup
	// responseTimes gets one entry for each page the run stores, when the page is settled.
	responseTimes ResponseTimes
}

func newFetchScheduler(cfg RunConfig) *fetchScheduler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = cfg.Workers
	return &fetchScheduler{
		cfg: cfg,
		client: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
			// Redirects are followed by the scheduler, one request at a time, so that each hop
			// is paced like any other request and paused by the host it leads to.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		limiter:   rate.NewLimiter(rate.Limit(cfg.Rate), 1),
		hostByKey: make(map[string]*hostQueue),
		// Never full: each request in flight sends exactly once, and there are at most
		// Workers of them.
		results: make(chan attemptResult, cfg.Workers),
	}
}

func (e *fetchScheduler) loop(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	interrupted := ctx.Done()
	var failure error
	for e.shouldContinue(runCtx) {
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
				// pile up pages that cannot be recorded. Cancelling is what stops the loop.
				failure = err
				cancel()
			}
		case <-wake:
		case <-interrupted:
			// Waking the loop is all that is needed: shouldContinue sees the cancelled
			// context and stops it.
			interrupted = nil
		}
		if timer != nil {
			timer.Stop()
		}
	}
	e.requests.Wait()
	return failure
}

// shouldContinue is asked before every round, so a cancelled context, whether the caller's
// or the one cancelled for a failure of the store, ends the loop as soon as what was in
// flight has come back, even when nothing is in flight and nothing will wake it.
func (e *fetchScheduler) shouldContinue(ctx context.Context) bool {
	if ctx.Err() != nil {
		e.stopping = true
	}
	return e.remaining > 0 && (!e.stopping || e.inFlight > 0)
}

// startReadyJobs starts everything that can start now and returns the moment something
// next may become startable, or the zero time when only a finished request can change
// anything.
func (e *fetchScheduler) startReadyJobs(ctx context.Context) time.Time {
	for e.inFlight < e.cfg.Workers && e.startOneReadyJob(ctx) {
	}
	if e.inFlight >= e.cfg.Workers {
		return time.Time{}
	}
	return e.earliestReadiness()
}

func (e *fetchScheduler) startOneReadyJob(ctx context.Context) bool {
	now := time.Now()
	for i := range e.hosts {
		position := (e.cursor + i) % len(e.hosts)
		host := e.hosts[position]
		if len(host.waiting) == 0 || now.Before(host.pausedUntil) {
			continue
		}
		// The token is asked for only once a host that may be asked is found, so a paused
		// host never spends one. Without a token no other host could start either.
		if !e.limiter.AllowN(now, 1) {
			return false
		}
		e.cursor = position + 1
		e.start(ctx, host.takeNext())
		return true
	}
	return false
}

// earliestReadiness is the moment the next request may start: once some host with an address
// waiting is out of its pause, and once the run's limiter has a token. It is the zero time when
// no host has anything waiting.
func (e *fetchScheduler) earliestReadiness() time.Time {
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
	if earliest.IsZero() {
		return earliest
	}
	if tokenAt := e.nextTokenAt(now); tokenAt.After(earliest) {
		return tokenAt
	}
	return earliest
}

// nextTokenAt is when the run's limiter has a token again. Looking does not spend the token;
// the reservation is handed straight back.
func (e *fetchScheduler) nextTokenAt(now time.Time) time.Time {
	reservation := e.limiter.ReserveN(now, 1)
	tokenAt := now.Add(reservation.DelayFrom(now))
	reservation.CancelAt(now)
	return tokenAt
}

func (e *fetchScheduler) start(ctx context.Context, job *pageJob) {
	job.startedAt = time.Now()
	e.inFlight++
	e.requests.Add(1)
	go func() {
		defer e.requests.Done()
		e.results <- e.attempt(ctx, job)
	}()
}

// settle applies what an attempt learned: the ledger row, the host's pause, and whether
// the address goes back in line. A non-nil error means the store failed.
func (e *fetchScheduler) settle(result attemptResult) error {
	e.inFlight--
	switch result.kind {
	case attemptCancelled:
		return nil
	case attemptBroken:
		return result.storeErr
	}
	job := result.job
	e.learnFromHost(job, result)
	if result.kind == attemptRedirect {
		return e.followRedirect(job, result)
	}
	switch result.kind {
	case attemptStored:
		e.remaining--
		if err := e.cfg.Store.MarkOK(job.address, result.outcome); err != nil {
			return err
		}
		e.responseTimes = append(e.responseTimes, result.responseTime)
		return nil
	case attemptFinal:
		e.remaining--
		return e.cfg.Store.MarkFailed(job.address, result.outcome)
	case attemptRefused:
		return e.waitOutRefusal(job, result)
	default:
		job.attempts++
		return e.tryAgainOrGiveUp(job, result)
	}
}

// waitOutRefusal keeps a refused address open. A refusal says the host wants to be asked
// later, not that the page is gone, so it spends none of the address's retries and never
// marks it failed: a page failed here would be skipped by every later run. What bounds a host
// that refuses for good is its own streak, and then the run stops asking that host.
func (e *fetchScheduler) waitOutRefusal(job *pageJob, result attemptResult) error {
	if err := e.cfg.Store.NoteAttempt(job.address, result.outcome); err != nil {
		return err
	}
	host := job.host
	if host.refusals >= maxRefusalsInARow {
		e.giveUpOnHost(host)
	}
	switch {
	case host.givenUp:
		e.remaining--
	case !e.stopping:
		host.requeue(job, true)
	}
	return nil
}

// giveUpOnHost leaves everything still waiting for a host pending, for a later run to ask
// again. The run then ends short of its list, which is what makes it exit non-zero.
func (e *fetchScheduler) giveUpOnHost(host *hostQueue) {
	if host.givenUp {
		return
	}
	host.givenUp = true
	e.remaining -= len(host.waiting)
	host.waiting = nil
}

// learnFromHost applies what an answer says about the host that gave it. A refusal rests the
// host. An answer of any other kind ends a streak of refusals, but only when the request it
// answers was sent after the last refusal: a late answer to a request that was already in
// flight when the host started refusing does not show that it has stopped.
func (e *fetchScheduler) learnFromHost(job *pageJob, result attemptResult) {
	host := job.host
	switch {
	case result.kind == attemptRefused:
		e.pauseHost(host, job, result)
	case result.outcome.HTTPStatus != 0 && job.startedAt.After(host.lastRefusalAt):
		host.refusals = 0
	}
}

// pauseHost rests a host that refused. The base is what the host asked for, or the default
// when it named nothing, and it doubles with each refusal in a row. A refusal is "in a row"
// when the request it answers was sent after the previous refusal: requests that were all in
// flight when the host began refusing are refused together, and that is one refusal, not as
// many as there were requests.
func (e *fetchScheduler) pauseHost(host *hostQueue, job *pageJob, result attemptResult) {
	base := e.cfg.RefusalPause
	if result.hasRetryAfter {
		base = result.retryAfter
	}
	if job.startedAt.After(host.lastRefusalAt) {
		host.refusals++
	}
	now := time.Now()
	host.lastRefusalAt = now
	if until := now.Add(refusalPause(base, host.refusals)); until.After(host.pausedUntil) {
		host.pausedUntil = until
	}
}

// followRedirect sends a job on to where it was redirected. The hop waits in the queue of the
// host it leads to, behind that host's pause and the run's limiter, so it is paced like any
// other request. It is not an attempt of its own: an address's attempts are the answers that were
// not just directions.
func (e *fetchScheduler) followRedirect(job *pageJob, result attemptResult) error {
	target := result.redirectTo
	job.hops++
	problem := ""
	switch {
	case job.hops > maxRedirectsPerAddress:
		problem = fmt.Sprintf("stopped after %d redirects", maxRedirectsPerAddress)
	case (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "":
		problem = "redirected to an address that cannot be fetched: " + target.String()
	}
	if problem != "" {
		job.attempts++
		e.remaining--
		result.outcome.Err = problem
		return e.cfg.Store.MarkFailed(job.address, result.outcome)
	}
	job.target = target
	job.host = e.hostFor(target)
	if !e.stopping {
		job.host.requeue(job, true)
	}
	return nil
}

func (e *fetchScheduler) tryAgainOrGiveUp(job *pageJob, result attemptResult) error {
	if job.attempts >= maxAttemptsPerRun {
		e.remaining--
		return e.cfg.Store.MarkFailed(job.address, result.outcome)
	}
	if err := e.cfg.Store.NoteAttempt(job.address, result.outcome); err != nil {
		return err
	}
	switch {
	case job.host.givenUp:
		e.remaining--
	case !e.stopping:
		job.host.requeue(job, false)
	}
	return nil
}
