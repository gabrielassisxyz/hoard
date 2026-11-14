package fetch_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

// A pause is a lower bound the scheduler must honour and, just as much, a time it must not
// overshoot: a host that is paused for 30s when it asked for 1s has been wronged too. The
// slack covers timer and scheduling lateness on a busy machine.
func pauseWithSlack(pause time.Duration) time.Duration {
	return pause + 800*time.Millisecond
}

func numberedAddresses(site *recordingSite, prefix string, count int) []string {
	addresses := make([]string, count)
	for i := range addresses {
		addresses[i] = site.address(fmt.Sprintf("/%s/%d", prefix, i))
	}
	return addresses
}

func TestRunNeverHasMoreRequestsInFlightThanWorkers(t *testing.T) {
	for _, workers := range []int{1, 3} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			t.Parallel()
			site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
				// Long enough that requests overlap whenever the scheduler lets them.
				time.Sleep(40 * time.Millisecond)
				writeSyntheticHTML(w, "<p>slow</p>")
			})
			st, _ := openFreshStore(t)
			addresses := numberedAddresses(site, "w", 12)

			summary := runFetchOver(t, st, addresses, func(cfg *fetch.RunConfig) { cfg.Workers = workers })

			if !summary.Complete() {
				t.Fatalf("summary = %v", summary)
			}
			if peak := site.peakInFlight(); peak != workers {
				t.Fatalf("the server saw up to %d requests at once, want exactly the ceiling of %d", peak, workers)
			}
		})
	}
}

func TestRunNeverAsksOneHostFasterThanItsRate(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		writeSyntheticHTML(w, "<p>quick</p>")
	})
	st, _ := openFreshStore(t)
	addresses := numberedAddresses(site, "r", 60)

	started := time.Now()
	summary := runFetchOver(t, st, addresses, func(cfg *fetch.RunConfig) {
		cfg.Rate = 50
		cfg.Workers = 8
	})
	elapsed := time.Since(started)

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	arrivals := site.arrivalTimes("")
	if len(arrivals) != 60 {
		t.Fatalf("%d requests for 60 addresses", len(arrivals))
	}
	if most := mostArrivalsInAnyWindow(arrivals, time.Second); most > 51 {
		t.Errorf("%d requests arrived within one second, at most 51 are allowed at 50 a second", most)
	}
	// The one-second window cannot tell a burst of one from a burst of two: the extra
	// request lands just past its edge. Requests 20ms apart never share a 5ms window, and
	// two sent back to back always do.
	if most := mostArrivalsInAnyWindow(arrivals, 5*time.Millisecond); most > 1 {
		t.Errorf("%d requests arrived within 5ms of one another at 50 a second: the limiter allows a burst", most)
	}
	if elapsed < 1100*time.Millisecond {
		t.Errorf("60 requests at 50 a second took %v, at least 1.1s is required", elapsed)
	}
}

func TestRunPacesEachHostOnItsOwn(t *testing.T) {
	serve := func(w http.ResponseWriter, r *http.Request) { writeSyntheticHTML(w, "<p>quick</p>") }
	first, second := newRecordingSite(t, serve), newRecordingSite(t, serve)
	st, _ := openFreshStore(t)
	addresses := append(numberedAddresses(first, "p", 6), numberedAddresses(second, "p", 6)...)

	started := time.Now()
	summary := runFetchOver(t, st, addresses, func(cfg *fetch.RunConfig) {
		cfg.Rate = 10
		cfg.Workers = 8
	})
	elapsed := time.Since(started)

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	for name, site := range map[string]*recordingSite{"first": first, "second": second} {
		arrivals := site.arrivalTimes("")
		if span := arrivals[len(arrivals)-1].Sub(arrivals[0]); span < 450*time.Millisecond {
			t.Errorf("%s host: 6 requests at 10 a second spanned %v, want at least 0.5s", name, span)
		}
	}
	// One limiter shared by both hosts would need 11 intervals of 100ms; one each needs 5.
	if elapsed >= time.Second {
		t.Errorf("12 requests over two hosts at 10 a second each took %v: the hosts are sharing one pace", elapsed)
	}
}

func TestRunWaitsOutARefusalBeforeAskingTheHostAgain(t *testing.T) {
	for _, refusal := range []int{http.StatusTooManyRequests, http.StatusForbidden} {
		t.Run(strconv.Itoa(refusal), func(t *testing.T) {
			t.Parallel()
			refusals := &failuresThenSuccess{n: 1}
			site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/limited" && refusals.stillFailing() {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(refusal)
					return
				}
				writeSyntheticHTML(w, "<p>"+r.URL.Path+"</p>")
			})
			st, _ := openFreshStore(t)
			// One worker keeps the order fixed: the refused address, then a neighbour on the
			// same host, which must also wait because the host is what was refused.
			summary := runFetchOver(t, st, site.addresses("/limited", "/neighbour"), func(cfg *fetch.RunConfig) {
				cfg.Workers = 1
			})

			if !summary.Complete() {
				t.Fatalf("summary = %v", summary)
			}
			if page := ledgerRowOf(t, st, site.address("/limited")); page.State != store.StateOK || page.Attempts != 2 {
				t.Errorf("refused address: row = %+v, want ok with two attempts", page)
			}
			limited, neighbour := site.arrivalTimes("/limited"), site.arrivalTimes("/neighbour")
			if len(limited) != 2 || len(neighbour) != 1 {
				t.Fatalf("requests: /limited %d, /neighbour %d", len(limited), len(neighbour))
			}
			if gap := limited[1].Sub(limited[0]); gap < time.Second || gap > pauseWithSlack(time.Second) {
				t.Errorf("the refused address was asked again after %v, Retry-After said 1s", gap)
			}
			if gap := neighbour[0].Sub(limited[0]); gap < time.Second || gap > pauseWithSlack(time.Second) {
				t.Errorf("another address on the refusing host was asked after %v, want it to wait out the 1s pause", gap)
			}
		})
	}
}

func TestRunDoublesThePauseOnConsecutiveRefusals(t *testing.T) {
	t.Parallel()
	refusals := &failuresThenSuccess{n: 2}
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		if refusals.stillFailing() {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeSyntheticHTML(w, "<p>finally</p>")
	})
	st, _ := openFreshStore(t)

	summary := runFetchOver(t, st, site.addresses("/stubborn"))

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	if page := ledgerRowOf(t, st, site.address("/stubborn")); page.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", page.Attempts)
	}
	arrivals := site.arrivalTimes("/stubborn")
	if len(arrivals) != 3 {
		t.Fatalf("%d requests, want 3", len(arrivals))
	}
	if first := arrivals[1].Sub(arrivals[0]); first < time.Second || first > pauseWithSlack(time.Second) {
		t.Errorf("after the first refusal the host was asked again after %v, want 1s", first)
	}
	if second := arrivals[2].Sub(arrivals[1]); second < 2*time.Second || second > pauseWithSlack(2*time.Second) {
		t.Errorf("after the second refusal in a row the host was asked again after %v, want the pause doubled to 2s", second)
	}
}

func TestRunFallsBackToItsOwnPauseWhenARefusalNamesNone(t *testing.T) {
	headers := map[string]string{"no header": "", "a header that is not a time": "soon"}
	for name, retryAfter := range headers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			refusals := &failuresThenSuccess{n: 1}
			site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
				if refusals.stillFailing() {
					if retryAfter != "" {
						w.Header().Set("Retry-After", retryAfter)
					}
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				writeSyntheticHTML(w, "<p>ok</p>")
			})
			st, _ := openFreshStore(t)

			summary := runFetchOver(t, st, site.addresses("/p"), func(cfg *fetch.RunConfig) {
				cfg.RefusalPause = time.Second
			})

			if !summary.Complete() {
				t.Fatalf("summary = %v", summary)
			}
			arrivals := site.arrivalTimes("/p")
			if len(arrivals) != 2 {
				t.Fatalf("%d requests, want 2", len(arrivals))
			}
			if gap := arrivals[1].Sub(arrivals[0]); gap < time.Second || gap > pauseWithSlack(time.Second) {
				t.Errorf("the host was asked again after %v, want the fallback pause of 1s", gap)
			}
		})
	}
}

func TestRunDoesNotDelayAnotherHostBehindARefusal(t *testing.T) {
	t.Parallel()
	refusals := &failuresThenSuccess{n: 1}
	refusing := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		if refusals.stillFailing() {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeSyntheticHTML(w, "<p>eventually</p>")
	})
	bystander := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		writeSyntheticHTML(w, "<p>never refused</p>")
	})
	st, _ := openFreshStore(t)
	// The refusing host is listed first and a single worker serves the list, so every
	// request to the bystander is made after the refusal has been received.
	addresses := append(refusing.addresses("/x"), numberedAddresses(bystander, "b", 3)...)

	summary := runFetchOver(t, st, addresses, func(cfg *fetch.RunConfig) { cfg.Workers = 1 })

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	refused := refusing.arrivalTimes("/x")
	served := bystander.arrivalTimes("")
	if len(refused) != 2 || len(served) != 3 {
		t.Fatalf("requests: refusing host %d, bystander %d", len(refused), len(served))
	}
	if last := served[len(served)-1]; last.Sub(refused[0]) >= time.Second {
		t.Errorf("the bystander's last request came %v after the refusal: it waited behind another host's pause", last.Sub(refused[0]))
	}
	if !served[len(served)-1].Before(refused[1]) {
		t.Errorf("the bystander was served after the refusing host's retry, which came %v after the refusal", refused[1].Sub(refused[0]))
	}
}

func TestRunStopsCleanlyWhenInterrupted(t *testing.T) {
	arrived := make(chan struct{}, 1)
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			arrived <- struct{}{}
			<-r.Context().Done()
			return
		}
		writeSyntheticHTML(w, "<p>not reached</p>")
	})
	st, _ := openFreshStore(t)
	addresses := site.addresses("/hang", "/after-1", "/after-2")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-arrived
		cancel()
	}()
	finished := make(chan struct{})
	var summary fetch.RunSummary
	var runErr error
	go func() {
		defer close(finished)
		summary, runErr = fetch.Run(ctx, fetch.RunConfig{
			Store: st, Addresses: addresses, Workers: 1, Rate: 1000, Timeout: time.Minute,
		})
	}()

	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after it was cancelled")
	}
	if runErr != nil {
		t.Fatalf("an interruption is not an error: %v", runErr)
	}
	if want := (fetch.RunSummary{Listed: 3, Pending: 3}); summary != want || summary.Complete() {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	if got := site.requestCount(); got != 1 {
		t.Errorf("%d requests, want only the one that was in flight", got)
	}
	for _, address := range addresses {
		if page := ledgerRowOf(t, st, address); page.State != store.StatePending {
			t.Errorf("%s is %s, want it left pending for the next run", address, page.State)
		}
	}
}
