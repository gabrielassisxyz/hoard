package fetch

import (
	"net/url"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/hostkey"
)

// pageJob is one address that still has to be fetched in this run.
type pageJob struct {
	address string   // as listed; the ledger's key
	target  *url.URL // what the next request asks for: the address, then wherever it was sent
	host    *hostQueue
	hops    int // redirects followed so far
	// attempts made in this run that count against its retries: answers other than a
	// redirect or a refusal.
	attempts int
	// startedAt is when the request now in flight was sent, which is how a late answer is told
	// from one given after the host's last refusal.
	startedAt time.Time
}

// hostQueue is everything the scheduler knows about one site: the addresses waiting for
// it and whether it has asked to be left alone. How often it is asked is not its own: one
// limiter paces the whole run. Only the scheduler goroutine touches it.
type hostQueue struct {
	waiting     []*pageJob
	pausedUntil time.Time
	// refusals is the length of the current streak of 429 and 403 answers to requests sent
	// one after another; an answer of any other kind to a later request ends the streak.
	refusals      int
	lastRefusalAt time.Time
	// givenUp is set once the streak reaches maxRefusalsInARow: the run asks this host for
	// nothing more, and what it still owed stays pending.
	givenUp bool
}

func (h *hostQueue) takeNext() *pageJob {
	job := h.waiting[0]
	h.waiting = h.waiting[1:]
	return job
}

// requeue puts a job back. A refused job goes to the front, since the pause already
// spaces the next try; a job that failed on the network goes to the back so the site gets
// time to recover before it is asked again.
func (h *hostQueue) requeue(job *pageJob, atFront bool) {
	if atFront {
		h.waiting = append([]*pageJob{job}, h.waiting...)
		return
	}
	h.waiting = append(h.waiting, job)
}

// readyAt is the earliest moment the host itself allows its next address to be asked for:
// after any pause it was given. When the run's limiter allows it is the scheduler's to add.
func (h *hostQueue) readyAt(now time.Time) time.Time {
	if h.pausedUntil.After(now) {
		return h.pausedUntil
	}
	return now
}

// hostKey names the site a request is queued and paused against. The rule lives in hostkey
// because a recipe has to name a site the same way the fetcher does.
func hostKey(u *url.URL) string {
	return hostkey.Of(u)
}
