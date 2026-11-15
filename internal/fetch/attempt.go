package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/hostkey"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

// What the scheduler needs to know about an attempt to decide what happens to its address.
type attemptKind int

const (
	attemptStored    attemptKind = iota // a 2xx whose body is now in the store
	attemptRefused                      // 429 or 403: the host wants to be left alone for a while
	attemptRedirect                     // a 3xx that says where to go next, which is a request of its own
	attemptRetryable                    // a network error or a 5xx: worth asking again
	attemptFinal                        // any other answer: asking again would not change it
	attemptCancelled                    // the run was interrupted before the host answered
	attemptBroken                       // the store could not take the body; the run cannot go on
)

type attemptResult struct {
	job           *pageJob
	kind          attemptKind
	outcome       store.Outcome
	retryAfter    time.Duration
	hasRetryAfter bool
	redirectTo    *url.URL
	storeErr      error
	// responseTime is set only on a stored page: how long the site took to answer the request
	// that ended in it, to the last byte of the body.
	responseTime time.Duration
}

// Draining a short error body lets the connection be reused; a long one is not worth the
// wait.
const maxDrainedErrorBodyBytes = 64 << 10

// attempt makes the one request that is next for a job and says what came of it. It never
// touches the ledger or a host's state: those belong to the scheduler, so a request in
// flight cannot race with the decision about the one before it.
func (e *fetchScheduler) attempt(ctx context.Context, job *pageJob) attemptResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, job.target.String(), nil)
	if err != nil {
		return attemptResult{job: job, kind: attemptFinal, outcome: store.Outcome{Err: err.Error()}}
	}
	req.Header.Set("User-Agent", e.cfg.UserAgent)
	// Asked of every request, a redirect's hops included, since each hop is built here: the
	// client follows nothing by itself and keeps no cookie jar.
	if e.cfg.Cookie != "" && hostkey.SessionMayGoTo(e.cfg.CookieHost, job.target) {
		req.Header.Set("Cookie", e.cfg.Cookie)
	}
	// The clock starts here and not when the job was queued or started: the wait for a worker
	// and for the run's limiter is pacing, and is over before this request exists.
	sentAt := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		return networkFailure(ctx, attemptResult{job: job}, "request failed: "+err.Error())
	}
	defer resp.Body.Close()

	result := attemptResult{job: job, outcome: store.Outcome{
		FinalAddress: resp.Request.URL.String(),
		HTTPStatus:   resp.StatusCode,
		FetchedAt:    time.Now(),
		ContentType:  resp.Header.Get("Content-Type"),
	}}
	redirectsTo, redirects := redirectTarget(resp)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return e.keepBody(ctx, resp, result, sentAt)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden:
		result.kind = attemptRefused
		result.retryAfter, result.hasRetryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case resp.StatusCode >= 500:
		result.kind = attemptRetryable
	case redirects:
		result.kind = attemptRedirect
		result.redirectTo = redirectsTo
	default:
		result.kind = attemptFinal
	}
	result.outcome.Err = "HTTP " + resp.Status
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainedErrorBodyBytes))
	return result
}

// redirectTarget is where a redirect that this tool follows points: the statuses browsers
// and the standard client follow, and only when the answer says where to.
func redirectTarget(resp *http.Response) (*url.URL, bool) {
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		target, err := resp.Location()
		return target, err == nil
	}
	return nil, false
}

// keepBody reads the answer to the end, within the size limit, and stores it. The body
// is what the transport hands over after its own decoding, byte for byte.
func (e *fetchScheduler) keepBody(ctx context.Context, resp *http.Response, result attemptResult, sentAt time.Time) attemptResult {
	if e.cfg.Images && !store.IsImageType(result.outcome.ContentType) {
		return refuseNonImage(result)
	}
	limit := e.cfg.MaxBodyBytes
	if resp.ContentLength > limit {
		return refuseOversizedBody(result, limit)
	}
	// One byte past the limit is enough to tell "exactly at it" from "over it".
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	// Read before the body is compressed and written: that is this machine's time, not the site's.
	answeredIn := time.Since(sentAt)
	if err != nil {
		return networkFailure(ctx, result, "read body: "+err.Error())
	}
	if int64(len(body)) > limit {
		return refuseOversizedBody(result, limit)
	}
	ref, err := e.cfg.ledger().PutBody(body, result.job.address, result.outcome.ContentType)
	if err != nil {
		result.kind = attemptBroken
		result.storeErr = err
		return result
	}
	result.kind = attemptStored
	result.outcome.Body = ref
	result.responseTime = answeredIn
	return result
}

// An error page served with status 200 is not a picture, and its body is not read.
func refuseNonImage(result attemptResult) attemptResult {
	result.kind = attemptFinal
	result.outcome.Err = fmt.Sprintf("not an image: Content-Type is %q", result.outcome.ContentType)
	return result
}

func refuseOversizedBody(result attemptResult, limit int64) attemptResult {
	result.kind = attemptFinal
	result.outcome.Err = fmt.Sprintf("body is larger than the %d byte limit", limit)
	return result
}

// networkFailure is an attempt that ended without a usable answer; result carries
// whatever was learned before it did. When the run itself was cancelled the failure is
// the cancellation's doing and says nothing about the host.
func networkFailure(ctx context.Context, result attemptResult, reason string) attemptResult {
	if ctx.Err() != nil {
		return attemptResult{job: result.job, kind: attemptCancelled}
	}
	result.kind = attemptRetryable
	result.outcome.Err = reason
	return result
}
