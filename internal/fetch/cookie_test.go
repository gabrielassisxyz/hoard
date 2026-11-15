package fetch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
)

// A session cookie belongs to one site. These tests stand two local servers side by side,
// one playing the site and one playing somebody else, and read what each was shown.

type cookieSighting struct {
	path   string
	values []string // every Cookie header of the request; nil when it had none
}

// cookieWitness is a server that remembers the Cookie headers of every request.
type cookieWitness struct {
	*httptest.Server
	mu        sync.Mutex
	sightings []cookieSighting
}

func newCookieWitness(t *testing.T, serve http.HandlerFunc) *cookieWitness {
	t.Helper()
	witness := &cookieWitness{}
	witness.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		witness.mu.Lock()
		witness.sightings = append(witness.sightings, cookieSighting{r.URL.Path, r.Header.Values("Cookie")})
		witness.mu.Unlock()
		serve(w, r)
	}))
	t.Cleanup(witness.Close)
	return witness
}

func (w *cookieWitness) seen() []cookieSighting {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]cookieSighting(nil), w.sightings...)
}

func (w *cookieWitness) hostKey() string {
	return strings.TrimPrefix(w.URL, "http://")
}

func servePlainPage(w http.ResponseWriter, r *http.Request) {
	writeSyntheticHTML(w, "<p>"+r.URL.Path+"</p>")
}

const siteCookie = "session=only-for-the-site; plan=paid"

func signedInAs(site *cookieWitness) func(*fetch.RunConfig) {
	return func(cfg *fetch.RunConfig) {
		cfg.Cookie = siteCookie
		cfg.CookieHost = site.hostKey()
	}
}

func wantEveryRequestSigned(t *testing.T, who string, sightings []cookieSighting, wantRequests int) {
	t.Helper()
	if len(sightings) != wantRequests {
		t.Fatalf("%s saw %d requests, want %d", who, len(sightings), wantRequests)
	}
	for _, sighting := range sightings {
		if len(sighting.values) != 1 || sighting.values[0] != siteCookie {
			t.Errorf("%s was asked for %s with Cookie %q, want exactly the configured one", who, sighting.path, sighting.values)
		}
	}
}

func wantNoRequestSigned(t *testing.T, who string, sightings []cookieSighting, wantRequests int) {
	t.Helper()
	if len(sightings) != wantRequests {
		t.Fatalf("%s saw %d requests, want %d", who, len(sightings), wantRequests)
	}
	for _, sighting := range sightings {
		if sighting.values != nil {
			t.Errorf("%s was asked for %s with a Cookie header (%d value(s)); the session is not its to see", who, sighting.path, len(sighting.values))
		}
	}
}

func TestRunSignsEveryRequestToTheSite(t *testing.T) {
	site := newCookieWitness(t, servePlainPage)
	st, _ := openFreshStore(t)
	addresses := []string{site.URL + "/a", site.URL + "/b", site.URL + "/c"}
	summary := runFetchOver(t, st, addresses, signedInAs(site))
	if !summary.Complete() {
		t.Fatalf("summary = %v, want everything held", summary)
	}
	wantEveryRequestSigned(t, "the site", site.seen(), 3)
}

func TestRunShowsTheCookieToNoOtherHost(t *testing.T) {
	other := newCookieWitness(t, servePlainPage)
	site := newCookieWitness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved-away" {
			http.Redirect(w, r, other.URL+"/landing", http.StatusFound)
			return
		}
		servePlainPage(w, r)
	})
	st, _ := openFreshStore(t)
	// One address on the site, one on the other host outright, and one the site itself
	// sends over to the other host: the hop of a redirect is the leak a test rarely stages.
	addresses := []string{site.URL + "/a", other.URL + "/listed", site.URL + "/moved-away"}
	summary := runFetchOver(t, st, addresses, signedInAs(site))
	if !summary.Complete() {
		t.Fatalf("summary = %v, want everything held", summary)
	}
	wantEveryRequestSigned(t, "the site", site.seen(), 2)
	wantNoRequestSigned(t, "the other host", other.seen(), 2)
}

func TestRunSignsTheHopThatComesBackToTheSite(t *testing.T) {
	var site *cookieWitness
	other := newCookieWitness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, site.URL+"/home", http.StatusFound)
	})
	site = newCookieWitness(t, servePlainPage)
	st, _ := openFreshStore(t)
	summary := runFetchOver(t, st, []string{other.URL + "/short-link"}, signedInAs(site))
	if !summary.Complete() {
		t.Fatalf("summary = %v, want everything held", summary)
	}
	wantNoRequestSigned(t, "the other host", other.seen(), 1)
	wantEveryRequestSigned(t, "the site", site.seen(), 1)
}

func TestRunNeverReplaysACookieAnAnswerSet(t *testing.T) {
	setsOne := func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "tracker", Value: "from-the-answer", Path: "/"})
		servePlainPage(w, r)
	}
	addressesOn := func(site *cookieWitness) []string {
		return []string{site.URL + "/a", site.URL + "/b", site.URL + "/c", site.URL + "/d"}
	}
	t.Run("signed in", func(t *testing.T) {
		site := newCookieWitness(t, setsOne)
		st, _ := openFreshStore(t)
		runFetchOver(t, st, addressesOn(site), signedInAs(site), func(cfg *fetch.RunConfig) { cfg.Workers = 1 })
		wantEveryRequestSigned(t, "the site", site.seen(), 4)
	})
	t.Run("signed out", func(t *testing.T) {
		site := newCookieWitness(t, setsOne)
		st, _ := openFreshStore(t)
		runFetchOver(t, st, addressesOn(site), func(cfg *fetch.RunConfig) { cfg.Workers = 1 })
		wantNoRequestSigned(t, "the site", site.seen(), 4)
	})
}

func TestRunRefusesACookieWithNoHostToBelongTo(t *testing.T) {
	st, _ := openFreshStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := fetch.Run(ctx, fetch.RunConfig{Store: st, Addresses: []string{"http://127.0.0.1:1/never-asked"}, Cookie: siteCookie})
	if err == nil {
		t.Fatal("fetch.Run accepted a cookie with no host to belong to")
	}
	if strings.Contains(err.Error(), "only-for-the-site") {
		t.Errorf("the error quotes the cookie: %v", err)
	}
	if _, found, err := st.Page("http://127.0.0.1:1/never-asked"); err != nil || found {
		t.Errorf("the refused run touched the ledger (row found=%v, err=%v)", found, err)
	}
}
