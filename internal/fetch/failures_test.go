package fetch_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

func TestRunTreatsNotFoundAndGoneAsFinal(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/gone":
			http.Error(w, "gone for good", http.StatusGone)
		default:
			writeSyntheticHTML(w, "<p>here</p>")
		}
	})
	addresses := site.addresses("/missing", "/gone", "/fine")
	dir := t.TempDir()

	first := openStoreAt(t, dir)
	summary := runFetchOver(t, first, addresses)
	if want := (fetch.RunSummary{Listed: 3, Held: 1, Failed: 2}); summary != want || summary.Complete() {
		t.Fatalf("summary = %+v, want %+v and not complete", summary, want)
	}
	for uri, status := range map[string]int{"/missing": 404, "/gone": 410} {
		page := ledgerRowOf(t, first, site.address(uri))
		if page.State != store.StateFailed || page.Attempts != 1 || page.HTTPStatus != status {
			t.Errorf("%s: row = %+v, want failed after one attempt with status %d", uri, page, status)
		}
		if !strings.Contains(page.LastError, http.StatusText(status)) {
			t.Errorf("%s: last error %q does not say what the site answered", uri, page.LastError)
		}
		if page.SHA256 != "" {
			t.Errorf("%s: a failed address has a body hash %s", uri, page.SHA256)
		}
		if got := len(site.arrivalTimes(uri)); got != 1 {
			t.Errorf("%s: requested %d times in the first run", uri, got)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := openStoreAt(t, dir)
	again := runFetchOver(t, second, addresses)
	if again != summary {
		t.Errorf("second run summary = %+v, want the same %+v", again, summary)
	}
	for _, uri := range []string{"/missing", "/gone", "/fine"} {
		if got := len(site.arrivalTimes(uri)); got != 1 {
			t.Errorf("%s: requested %d times over two runs, a settled address must not be asked again", uri, got)
		}
	}
}

func TestRunRetriesServerErrorsUpToThreeAttempts(t *testing.T) {
	flaky := &failuresThenSuccess{n: 2}
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/flaky":
			if flaky.stillFailing() {
				http.Error(w, "try later", http.StatusInternalServerError)
				return
			}
			writeSyntheticHTML(w, "<p>recovered</p>")
		case "/down":
			http.Error(w, "still broken", http.StatusInternalServerError)
		case "/unavailable":
			http.Error(w, "overloaded", http.StatusServiceUnavailable)
		}
	})
	st, _ := openFreshStore(t)

	summary := runFetchOver(t, st, site.addresses("/flaky", "/down", "/unavailable"))

	if want := (fetch.RunSummary{Listed: 3, Held: 1, Failed: 2}); summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	if page := ledgerRowOf(t, st, site.address("/flaky")); page.State != store.StateOK || page.Attempts != 3 || page.LastError != "" {
		t.Errorf("/flaky: row = %+v, want ok with three attempts and no error left on it", page)
	}
	for uri, status := range map[string]int{"/down": 500, "/unavailable": 503} {
		page := ledgerRowOf(t, st, site.address(uri))
		if page.State != store.StateFailed || page.Attempts != 3 || page.HTTPStatus != status {
			t.Errorf("%s: row = %+v, want failed after three attempts", uri, page)
		}
		if !strings.Contains(page.LastError, "HTTP 5") {
			t.Errorf("%s: last error %q", uri, page.LastError)
		}
		if got := len(site.arrivalTimes(uri)); got != 3 {
			t.Errorf("%s: %d requests, want exactly 3", uri, got)
		}
	}
}

func TestRunRetriesNetworkErrorsUpToThreeAttempts(t *testing.T) {
	flaky := &failuresThenSuccess{n: 2}
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reset-twice":
			if flaky.stillFailing() {
				dropConnection(w)
				return
			}
			writeSyntheticHTML(w, "<p>third time</p>")
		case "/always-reset":
			dropConnection(w)
		}
	})
	vanished := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {})
	vanishedAddress := vanished.address("/nobody-home")
	vanished.Close()
	st, _ := openFreshStore(t)

	summary := runFetchOver(t, st, append(site.addresses("/reset-twice", "/always-reset"), vanishedAddress))

	if want := (fetch.RunSummary{Listed: 3, Held: 1, Failed: 2}); summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	if page := ledgerRowOf(t, st, site.address("/reset-twice")); page.State != store.StateOK || page.Attempts != 3 {
		t.Errorf("/reset-twice: row = %+v", page)
	}
	for _, address := range []string{site.address("/always-reset"), vanishedAddress} {
		page := ledgerRowOf(t, st, address)
		if page.State != store.StateFailed || page.Attempts != 3 || page.HTTPStatus != 0 || page.LastError == "" {
			t.Errorf("%s: row = %+v, want failed after three attempts with an error and no status", address, page)
		}
	}
	// The count of attempts is the ledger's. The server may see one more arrival than that:
	// when a kept-alive connection turns out to be dead, the transport itself replays a GET
	// once on a fresh one, below the level this tool counts at.
	if got := len(site.arrivalTimes("/always-reset")); got < 3 {
		t.Errorf("/always-reset: %d requests, want at least one per attempt", got)
	}
}

func TestRunRefusesABodyOverTheLimitAndStoresNothingForIt(t *testing.T) {
	const limit = 1024
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/at-the-limit":
			w.Write([]byte(strings.Repeat("x", limit)))
		case "/declared-over":
			w.Header().Set("Content-Length", "4096")
			w.Write([]byte(strings.Repeat("x", 4096)))
		case "/streamed-over":
			// No Content-Length: the size is only known by reading.
			for range 8 {
				w.Write([]byte(strings.Repeat("x", 512)))
				w.(http.Flusher).Flush()
			}
		}
	})
	st, dir := openFreshStore(t)

	summary := runFetchOver(t, st, site.addresses("/at-the-limit", "/declared-over", "/streamed-over"),
		func(cfg *fetch.RunConfig) { cfg.MaxBodyBytes = limit })

	if want := (fetch.RunSummary{Listed: 3, Held: 1, Failed: 2}); summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	if page := ledgerRowOf(t, st, site.address("/at-the-limit")); page.State != store.StateOK || page.SizeBytes != limit {
		t.Errorf("a body of exactly the limit: row = %+v", page)
	}
	for _, uri := range []string{"/declared-over", "/streamed-over"} {
		page := ledgerRowOf(t, st, site.address(uri))
		if page.State != store.StateFailed || page.Attempts != 1 || !strings.Contains(page.LastError, "larger than") {
			t.Errorf("%s: row = %+v, want failed after one attempt, naming the limit", uri, page)
		}
		if got := len(site.arrivalTimes(uri)); got != 1 {
			t.Errorf("%s: %d requests, an oversized body is not worth asking for again", uri, got)
		}
	}
	if files := bodyFilesUnder(t, dir); len(files) != 1 {
		t.Errorf("%d body files, want only the one under the limit: %v", len(files), files)
	}
}

func TestRunGivesUpOnARequestThatOutlastsTheTimeout(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		writeSyntheticHTML(w, "<p>quick</p>")
	})
	st, _ := openFreshStore(t)

	started := time.Now()
	summary := runFetchOver(t, st, site.addresses("/slow", "/quick"), func(cfg *fetch.RunConfig) {
		cfg.Timeout = 150 * time.Millisecond
	})

	if want := (fetch.RunSummary{Listed: 2, Held: 1, Failed: 1}); summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	if page := ledgerRowOf(t, st, site.address("/slow")); page.State != store.StateFailed || page.Attempts != 3 {
		t.Errorf("/slow: row = %+v, want failed after three timed-out attempts", page)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Errorf("three attempts at a 150ms timeout took %v: the timeout is not being applied", elapsed)
	}
}
