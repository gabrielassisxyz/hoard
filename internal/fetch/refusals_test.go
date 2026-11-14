package fetch_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

func TestRunDoesNotEscalateThePauseForRefusalsAlreadyInFlight(t *testing.T) {
	t.Parallel()
	refusals := &failuresThenSuccess{n: 4}
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		if refusals.stillFailing() {
			// Slow enough that all four requests are in flight when the first refusal lands.
			time.Sleep(300 * time.Millisecond)
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeSyntheticHTML(w, "<p>"+r.URL.Path+"</p>")
	})
	st, _ := openFreshStore(t)

	summary := runFetchOver(t, st, numberedAddresses(site, "wave", 4), func(cfg *fetch.RunConfig) {
		cfg.Workers = 4
	})

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	arrivals := site.arrivalTimes("")
	if len(arrivals) != 8 {
		t.Fatalf("%d requests, want the four refused and the four retried", len(arrivals))
	}
	// Four refusals answered one wave of requests. The host asked for one second, not for one
	// second doubled for each request that happened to be in flight, which would be eight.
	if gap := arrivals[4].Sub(arrivals[3]); gap > pauseWithSlack(1300*time.Millisecond) {
		t.Errorf("the refused wave was retried %v after it was sent; the host asked for 1s after its 300ms answer", gap)
	}
}

func TestRunStartsTheStreakOverOnceTheHostAnswersNormally(t *testing.T) {
	t.Parallel()
	var aRefused, bRefused failuresThenSuccess
	aRefused.n, bRefused.n = 1, 1
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		refusal := map[string]*failuresThenSuccess{"/a": &aRefused, "/b": &bRefused}[r.URL.Path]
		if refusal != nil && refusal.stillFailing() {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeSyntheticHTML(w, "<p>"+r.URL.Path+"</p>")
	})
	st, _ := openFreshStore(t)

	// One worker fixes the order: /a is refused, retried and served; then /b is refused.
	summary := runFetchOver(t, st, site.addresses("/a", "/b"), func(cfg *fetch.RunConfig) { cfg.Workers = 1 })

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	b := site.arrivalTimes("/b")
	if len(b) != 2 {
		t.Fatalf("/b was requested %d times, want 2", len(b))
	}
	// Had the 200 for /a not ended the streak, this second refusal would count as the second
	// in a row and rest the host for 2s.
	if gap := b[1].Sub(b[0]); gap < time.Second || gap > pauseWithSlack(time.Second) {
		t.Errorf("after a refusal that followed a normal answer the host was asked again after %v, want 1s", gap)
	}
}

func TestRunStopsAskingForAnAddressAHostKeepsRefusing(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusForbidden)
	})
	st, _ := openFreshStore(t)

	summary := runFetchOver(t, st, site.addresses("/walled"))

	if want := (fetch.RunSummary{Listed: 1, Failed: 1}); summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	page := ledgerRowOf(t, st, site.address("/walled"))
	if page.State != store.StateFailed || page.Attempts != 3 || !strings.Contains(page.LastError, "403") {
		t.Errorf("row = %+v, want failed after three attempts naming the 403", page)
	}
	if got := site.requestCount(); got != 3 {
		t.Errorf("%d requests, want exactly 3", got)
	}
}

func TestRunStopsAndReportsWhenTheStoreCannotTakeABody(t *testing.T) {
	const page = "<p>cannot be stored</p>"
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		writeSyntheticHTML(w, page)
	})
	st, dir := openFreshStore(t)
	// A file where the body's directory belongs: creating the directory fails, on any
	// platform and whoever runs the test.
	blocker := filepath.Join(dir, "raw", hexSHA256OfBytes([]byte(page))[:2])
	if err := os.WriteFile(blocker, []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	addresses := numberedAddresses(site, "p", 5)

	summary, err := fetch.Run(t.Context(), fetch.RunConfig{
		Store: st, Addresses: addresses, Workers: 1, Rate: 1000, Timeout: 5 * time.Second,
	})

	if err == nil || !strings.Contains(err.Error(), "store body") {
		t.Fatalf("Run returned %v, want the store's error", err)
	}
	if want := (fetch.RunSummary{Listed: 5, Pending: 5}); summary != want {
		t.Errorf("summary = %+v, want %+v: nothing could be recorded", summary, want)
	}
	if got := site.requestCount(); got != 1 {
		t.Errorf("%d requests after the store failed, want the run to stop after the first", got)
	}
}
