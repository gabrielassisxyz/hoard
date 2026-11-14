package fetch_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

// A redirect is a request to a host like any other. A site that redirects every address, as
// many do from http to https or to a canonical host name, must not be asked twice as fast as
// the pace that was set for it, nor be reached through a back door around its pause.

func TestRunPacesEveryHopOfARedirect(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, "/old/"); ok {
			http.Redirect(w, r, "/landing/"+rest, http.StatusFound)
			return
		}
		writeSyntheticHTML(w, "<p>"+r.URL.Path+"</p>")
	})
	st, _ := openFreshStore(t)
	addresses := numberedAddresses(site, "old", 6)

	started := time.Now()
	summary := runFetchOver(t, st, addresses, func(cfg *fetch.RunConfig) {
		cfg.Rate = 20
		cfg.Workers = 4
	})
	elapsed := time.Since(started)

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	arrivals := site.arrivalTimes("")
	if len(arrivals) != 12 {
		t.Fatalf("%d requests for 6 addresses that redirect once each, want 12", len(arrivals))
	}
	// Twelve requests at 20 a second are eleven intervals of 50ms. If the redirects ride along
	// with the request that led to them, only the six first hops are paced and it takes five.
	if elapsed < 500*time.Millisecond {
		t.Errorf("12 requests at 20 a second finished in %v: the redirect hops are not being paced", elapsed)
	}
	if most := mostArrivalsInAnyWindow(arrivals, 100*time.Millisecond); most > 3 {
		t.Errorf("%d requests arrived within 100ms at 20 a second, at most 3 are allowed", most)
	}
}

func TestRunPacesARedirectHopAgainstTheHostItLeadsTo(t *testing.T) {
	t.Parallel()
	refusals := &failuresThenSuccess{n: 1}
	refusing := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" && refusals.stillFailing() {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeSyntheticHTML(w, "<p>"+r.URL.Path+"</p>")
	})
	redirecting := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, refusing.address("/landing"), http.StatusFound)
	})
	st, _ := openFreshStore(t)
	// One worker fixes the order: the refusing host is refused first, so that the redirect
	// from the other host arrives while the host it leads to is resting.
	addresses := []string{refusing.address("/first"), redirecting.address("/hop")}

	summary := runFetchOver(t, st, addresses, func(cfg *fetch.RunConfig) { cfg.Workers = 1 })

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	refused, landed := refusing.arrivalTimes("/first"), refusing.arrivalTimes("/landing")
	if len(refused) != 2 || len(landed) != 1 {
		t.Fatalf("requests to the refusing host: /first %d, /landing %d", len(refused), len(landed))
	}
	if gap := landed[0].Sub(refused[0]); gap < 2*time.Second {
		t.Errorf("a redirect reached the refusing host %v after its refusal, inside the 2s pause", gap)
	}
}

func TestRunGivesUpOnARedirectLoopWithoutRetryingIt(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	st, _ := openFreshStore(t)

	summary := runFetchOver(t, st, site.addresses("/loop"))

	if want := (fetch.RunSummary{Listed: 1, Failed: 1}); summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	page := ledgerRowOf(t, st, site.address("/loop"))
	if page.State != store.StateFailed || page.Attempts != 1 || !strings.Contains(page.LastError, "redirects") {
		t.Errorf("row = %+v, want failed after one attempt, naming the redirects", page)
	}
	// The address itself and ten hops; asking again would only walk the same circle.
	if got := site.requestCount(); got != 11 {
		t.Errorf("%d requests, want the address and 10 redirects", got)
	}
}

func TestRunSettlesARedirectToSomewhereItCannotGo(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-ftp":
			http.Redirect(w, r, "ftp://files.test/archive", http.StatusFound)
		case "/no-location":
			w.WriteHeader(http.StatusFound)
		default:
			writeSyntheticHTML(w, "<p>fine</p>")
		}
	})
	st, _ := openFreshStore(t)

	summary := runFetchOver(t, st, site.addresses("/to-ftp", "/no-location", "/fine"))

	if want := (fetch.RunSummary{Listed: 3, Held: 1, Failed: 2}); summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	for _, uri := range []string{"/to-ftp", "/no-location"} {
		page := ledgerRowOf(t, st, site.address(uri))
		if page.State != store.StateFailed || page.Attempts != 1 {
			t.Errorf("%s: row = %+v, want failed after one attempt", uri, page)
		}
		if got := len(site.arrivalTimes(uri)); got != 1 {
			t.Errorf("%s: %d requests, want 1", uri, got)
		}
	}
	if page := ledgerRowOf(t, st, site.address("/to-ftp")); !strings.Contains(page.LastError, "ftp://files.test/archive") {
		t.Errorf("last error %q does not name the address it was sent to", page.LastError)
	}
}

func TestRunFollowsAChainOfRedirectsAcrossHosts(t *testing.T) {
	final := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		writeSyntheticHTML(w, "<p>the end of the chain</p>")
	})
	middle := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.address("/end"), http.StatusTemporaryRedirect)
	})
	first := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, middle.address("/middle"), http.StatusMovedPermanently)
	})
	st, dir := openFreshStore(t)

	summary := runFetchOver(t, st, first.addresses("/start"))

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	page := ledgerRowOf(t, st, first.address("/start"))
	if page.Attempts != 1 || page.FinalAddress != final.address("/end") {
		t.Errorf("row = %+v, want one attempt ending at %s", page, final.address("/end"))
	}
	if got := decompressedBodyFile(t, storedBodyFile(dir, page.SHA256)); !strings.Contains(string(got), "the end of the chain") {
		t.Errorf("stored body = %q", got)
	}
	for name, site := range map[string]*recordingSite{"first": first, "middle": middle, "final": final} {
		if got := site.requestCount(); got != 1 {
			t.Errorf("%s host saw %d requests, want 1", name, got)
		}
	}
}
