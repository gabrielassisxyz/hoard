package fetch

import (
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// pageJob is one address that still has to be fetched in this run.
type pageJob struct {
	address  string
	host     *hostQueue
	attempts int // made in this run, whatever their outcome
}

// hostQueue is everything the scheduler knows about one site: the addresses waiting for
// it, the limiter that spaces its requests, and whether it has asked to be left alone.
// Only the scheduler goroutine touches it.
type hostQueue struct {
	limiter     *rate.Limiter
	waiting     []*pageJob
	pausedUntil time.Time
	// refusals is the length of the current streak of 429 and 403 answers; any other
	// answer from the host ends the streak.
	refusals int
}

func newHostQueue(requestsPerSecond float64) *hostQueue {
	return &hostQueue{limiter: rate.NewLimiter(rate.Limit(requestsPerSecond), 1)}
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

// readyAt is the earliest moment this host may be asked for its next address: after any
// pause it was given, and once the limiter has a token again. Looking does not spend the
// token; the reservation is handed straight back.
func (h *hostQueue) readyAt(now time.Time) time.Time {
	at := now
	if h.pausedUntil.After(at) {
		at = h.pausedUntil
	}
	reservation := h.limiter.ReserveN(now, 1)
	tokenAt := now.Add(reservation.DelayFrom(now))
	reservation.CancelAt(now)
	if tokenAt.After(at) {
		at = tokenAt
	}
	return at
}

// hostKey names the site a request is paced against: the host and port a request is sent
// to, in lower case, with the port left out when it is the scheme's own. Two spellings of
// one site must share a limiter, or the stated pace would quietly be doubled.
func hostKey(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" || port == defaultPortOf(u.Scheme) {
		return host
	}
	return net.JoinHostPort(host, port)
}

func defaultPortOf(scheme string) string {
	switch strings.ToLower(scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}
