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
	return pause + 4*time.Second
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
				// Long enough that requests overlap whenever the scheduler lets them, even on a
				// busy machine.
				time.Sleep(100 * time.Millisecond)
				writeSyntheticHTML(w, "<p>slow</p>")
			})
			st, _ := openFreshStore(t)
			addresses := numberedAddresses(site, "w", 9)

			summary := runFetchOver(t, st, addresses, func(cfg *fetch.RunConfig) { cfg.Workers = workers })

			if !summary.Complete() {
				t.Fatalf("summary = %v", summary)
			}
			peak := site.peakInFlight()
			if peak > workers {
				t.Fatalf("the server saw %d requests at once, more than the ceiling of %d", peak, workers)
			}
			// Not vacuous: requests do overlap when more than one is allowed. Reaching the
			// ceiling itself would depend on the machine not stalling between the starts.
			if workers > 1 && peak < 2 {
				t.Fatalf("the server never saw two requests at once with %d workers", workers)
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
	if elapsed < 1100*time.Millisecond {
		t.Errorf("60 requests at 50 a second took %v, at least 1.1s is required", elapsed)
	}
}

func TestRunPacesTwoHostsByTheOneRateOfTheRun(t *testing.T) {
	serve := func(w http.ResponseWriter, r *http.Request) { writeSyntheticHTML(w, "<p>quick</p>") }
	first, second := newRecordingSite(t, serve), newRecordingSite(t, serve)
	st, _ := openFreshStore(t)
	addresses := append(numberedAddresses(first, "p", 6), numberedAddresses(second, "p", 6)...)

	summary := runFetchOver(t, st, addresses, func(cfg *fetch.RunConfig) {
		cfg.Rate = 10
		cfg.Workers = 8
	})

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	arrivals := arrivalsAcross(first, second)
	if len(arrivals) != 12 {
		t.Fatalf("%d requests for 12 addresses", len(arrivals))
	}
	// Twelve requests at 10 a second are eleven intervals of 100ms, whichever host each one is
	// for. Paced per host, the two are asked side by side and all twelve fit in the half second
	// that six take.
	if span := arrivals[len(arrivals)-1].Sub(arrivals[0]); span < 1050*time.Millisecond {
		t.Errorf("12 requests to two hosts at 10 a second spanned %v, want at least 1.1s: each host is paced on its own", span)
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

func TestRunReadsARetryAfterGivenAsAnHTTPDate(t *testing.T) {
	t.Parallel()
	refusals := &failuresThenSuccess{n: 1}
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		if refusals.stillFailing() {
			// Dates carry whole seconds, so the wait the host actually asks for is between
			// one and two seconds, depending on where in the second the refusal was sent.
			w.Header().Set("Retry-After", time.Now().Add(2*time.Second).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeSyntheticHTML(w, "<p>after the date</p>")
	})
	st, _ := openFreshStore(t)

	summary := runFetchOver(t, st, site.addresses("/dated"), func(cfg *fetch.RunConfig) {
		cfg.RefusalPause = 20 * time.Second
	})

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	arrivals := site.arrivalTimes("/dated")
	if len(arrivals) != 2 {
		t.Fatalf("%d requests, want 2", len(arrivals))
	}
	if gap := arrivals[1].Sub(arrivals[0]); gap < time.Second || gap > pauseWithSlack(2*time.Second) {
		t.Errorf("the host was asked again after %v, want the 1s to 2s that the date asked for and not the 20s fallback", gap)
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
			w.Header().Set("Retry-After", "6")
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
	// The pause is 6s. Served in well under half of it is the bystander not waiting for it;
	// the margin is for a machine that stalls, and a shared pause would still miss it by 3s.
	if last := served[len(served)-1]; last.Sub(refused[0]) >= 3*time.Second {
		t.Errorf("the bystander's last request came %v after the refusal: it waited behind another host's pause", last.Sub(refused[0]))
	}
	if !served[len(served)-1].Before(refused[1]) {
		t.Errorf("the bystander was served after the refusing host's retry, which came %v after the refusal", refused[1].Sub(refused[0]))
	}
}

func TestRunKeepsTheOtherHostsToTheRateWhileOneRests(t *testing.T) {
	refusals := &failuresThenSuccess{n: 1}
	resting := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		if refusals.stillFailing() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeSyntheticHTML(w, "<p>after the rest</p>")
	})
	bystander := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		writeSyntheticHTML(w, "<p>never refused</p>")
	})
	st, _ := openFreshStore(t)
	// Listed first, the resting host is asked first, and its refusal is the run's first answer.
	addresses := append(numberedAddresses(resting, "r", 6), numberedAddresses(bystander, "b", 12)...)
	const pause = 400 * time.Millisecond

	summary := runFetchOver(t, st, addresses, func(cfg *fetch.RunConfig) {
		cfg.Rate = 20
		cfg.Workers = 4
		cfg.RefusalPause = pause
	})

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	rested, served := resting.arrivalTimes(""), bystander.arrivalTimes("")
	if len(rested) != 7 || len(served) != 12 {
		t.Fatalf("requests: resting host %d, bystander %d", len(rested), len(served))
	}
	if gap := rested[1].Sub(rested[0]); gap < pause || gap > pauseWithSlack(pause) {
		t.Errorf("the refusing host was asked again after %v, want its own pause of %v", gap, pause)
	}
	askedDuringThePause := 0
	for _, at := range served {
		if at.After(rested[0]) && at.Before(rested[1]) {
			askedDuringThePause++
		}
	}
	if askedDuringThePause < 2 {
		t.Errorf("the bystander was asked %d times while the other host rested: it waited out a pause that was not its own", askedDuringThePause)
	}
	// Nineteen requests at 20 a second are eighteen intervals of 50ms, whichever host each was
	// for. Paced per host, the bystander's twelve run beside the other host's seven.
	all := arrivalsAcross(resting, bystander)
	if span := all[len(all)-1].Sub(all[0]); span < 875*time.Millisecond {
		t.Errorf("19 requests to two hosts at 20 a second spanned %v, want at least 0.9s: each host is paced on its own", span)
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

	summary, runErr := runFetchGuarded(t, ctx, fetch.RunConfig{
		Store: st, Addresses: addresses, Workers: 1, Rate: 1000, Timeout: time.Minute,
	}, 10*time.Second)

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
