package fetch_test

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

func TestRunStoresEveryBodyRawAndContentAddressed(t *testing.T) {
	sent := map[string]string{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		sent["/page/"+name] = syntheticPage("page " + name)
	}
	sent["/same/one"] = "<p>identical bytes</p>"
	sent["/same/two"] = "<p>identical bytes</p>"
	sent["/empty"] = ""
	gzipped := syntheticPage("served compressed on the wire")
	sent["/gzip"] = gzipped

	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gzip" {
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				http.Error(w, "this site only answers compressed", http.StatusNotAcceptable)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Encoding", "gzip")
			var wire bytes.Buffer
			zw := gzip.NewWriter(&wire)
			zw.Write([]byte(gzipped))
			zw.Close()
			w.Write(wire.Bytes())
			return
		}
		serveHTML(w, sent[r.URL.Path])
	})
	var requestURIs []string
	for uri := range sent {
		requestURIs = append(requestURIs, uri)
	}
	addresses := site.addresses(requestURIs...)
	st, dir := openFreshStore(t)

	started := time.Now()
	summary := runFetchOver(t, st, addresses)

	if want := (fetch.RunSummary{Listed: len(addresses), Held: len(addresses)}); summary != want || !summary.Complete() {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	pages, err := st.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != len(addresses) {
		t.Fatalf("%d rows for %d listed addresses", len(pages), len(addresses))
	}
	distinctBodies := map[string]bool{}
	for _, page := range pages {
		uri := strings.TrimPrefix(page.Address, site.URL)
		want := []byte(sent[uri])
		if page.State != store.StateOK || page.Attempts != 1 || page.HTTPStatus != http.StatusOK {
			t.Errorf("%s: row = %+v, want ok after one attempt with status 200", uri, page)
		}
		if page.ContentType != "text/html; charset=utf-8" && uri != "/gzip" && uri != "/empty" {
			t.Errorf("%s: content type = %q", uri, page.ContentType)
		}
		if page.FetchedAt.Before(started.Add(-time.Second)) || page.FetchedAt.After(time.Now().Add(time.Second)) || page.FetchedAt.Location() != time.UTC {
			t.Errorf("%s: fetched at %v in %v", uri, page.FetchedAt, page.FetchedAt.Location())
		}

		path := storedBodyFile(dir, page.SHA256)
		got := decompressedBodyFile(t, path)
		if !bytes.Equal(got, want) {
			t.Errorf("%s: the file decompresses to %q, the server sent %q", uri, got, want)
		}
		if sha256HexOf(got) != page.SHA256 {
			t.Errorf("%s: the decompressed bytes hash to %s, not to the file name %s", uri, sha256HexOf(got), page.SHA256)
		}
		if page.SizeBytes != int64(len(want)) {
			t.Errorf("%s: size = %d, want %d", uri, page.SizeBytes, len(want))
		}
		distinctBodies[page.SHA256] = true
	}
	if files := bodyFilesUnder(t, dir); len(files) != len(distinctBodies) {
		t.Errorf("%d body files for %d distinct bodies: %v", len(files), len(distinctBodies), files)
	}
}

func TestSecondRunOverTheSameListAndStoreMakesNoRequests(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		serveHTML(w, syntheticPage(r.URL.Path))
	})
	addresses := site.addresses("/1", "/2", "/3", "/4", "/5")
	dir := t.TempDir()

	first := openStoreAt(t, dir)
	if summary := runFetchOver(t, first, addresses); !summary.Complete() {
		t.Fatalf("first run: %v", summary)
	}
	if got := site.requestCount(); got != len(addresses) {
		t.Fatalf("first run made %d requests for %d addresses", got, len(addresses))
	}
	before, _ := first.Pages()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// A new store handle stands in for a new process: only what is on disk can stop the
	// second run from asking again.
	second := openStoreAt(t, dir)
	summary := runFetchOver(t, second, addresses)
	if !summary.Complete() || summary.Held != len(addresses) {
		t.Fatalf("second run: %v", summary)
	}
	if got := site.requestCount(); got != len(addresses) {
		t.Fatalf("the second run made %d more requests", got-len(addresses))
	}
	after, _ := second.Pages()
	if len(after) != len(before) {
		t.Fatalf("rows went from %d to %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("row changed on the second run:\n before %+v\n after  %+v", before[i], after[i])
		}
	}
}

func TestRunRecordsBothAddressesOfARedirect(t *testing.T) {
	landing := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		serveHTML(w, syntheticPage("the landing page"))
	})
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/moved/new", http.StatusMovedPermanently)
		case "/hop":
			http.Redirect(w, r, landing.address("/landing"), http.StatusFound)
		default:
			serveHTML(w, syntheticPage("the page that moved"))
		}
	})
	st, dir := openFreshStore(t)

	summary := runFetchOver(t, st, site.addresses("/old", "/hop"))
	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	for requested, final := range map[string]string{
		site.address("/old"): site.address("/moved/new"),
		site.address("/hop"): landing.address("/landing"),
	} {
		page := rowFor(t, st, requested)
		if page.State != store.StateOK || page.Attempts != 1 {
			t.Errorf("%s: row = %+v", requested, page)
		}
		if page.Address != requested || page.FinalAddress != final {
			t.Errorf("%s: recorded address %q and final address %q, want %q and %q",
				requested, page.Address, page.FinalAddress, requested, final)
		}
		if page.Address == page.FinalAddress {
			t.Errorf("%s: requested and final address are the same", requested)
		}
		if got := decompressedBodyFile(t, storedBodyFile(dir, page.SHA256)); !strings.Contains(string(got), "<title>") {
			t.Errorf("%s: the stored body is %q, not the page the redirect led to", requested, got)
		}
	}
}

func TestRunIntroducesItselfWithTheConfiguredUserAgent(t *testing.T) {
	cases := map[string]string{
		"default":  fetch.DefaultUserAgent,
		"override": "a-different-agent/9.9",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
				serveHTML(w, "<p>hi</p>")
			})
			st, _ := openFreshStore(t)
			runFetchOver(t, st, site.addresses("/a", "/b"), func(cfg *fetch.RunConfig) {
				if name == "override" {
					cfg.UserAgent = want
				}
			})
			for _, got := range site.userAgents() {
				if got != want {
					t.Errorf("User-Agent = %q, want %q", got, want)
				}
			}
		})
	}
	if !strings.Contains(fetch.DefaultUserAgent, "hoard") || !strings.Contains(fetch.DefaultUserAgent, "https://github.com/gabrielassisxyz/hoard") {
		t.Errorf("the default User-Agent %q names neither the tool nor its repository", fetch.DefaultUserAgent)
	}
}

func TestRunSettlesAddressesItCannotRequestWithoutRequestingThem(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		serveHTML(w, "<p>fine</p>")
	})
	unrequestable := []string{"not an address", "ftp://files.test/archive", "http:///no-host", "/relative/path"}
	st, _ := openFreshStore(t)

	summary := runFetchOver(t, st, append(unrequestable, site.address("/fine")))
	if want := (fetch.RunSummary{Listed: 5, Held: 1, Failed: 4}); summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	if got := site.requestCount(); got != 1 {
		t.Fatalf("%d requests, want only the one for the fetchable address", got)
	}
	for _, address := range unrequestable {
		page := rowFor(t, st, address)
		if page.State != store.StateFailed || page.Attempts != 0 || !strings.Contains(page.LastError, "invalid address") {
			t.Errorf("%q: row = %+v", address, page)
		}
	}
}

func TestRunRequestsAnAddressListedTwiceOnce(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		serveHTML(w, "<p>once</p>")
	})
	st, _ := openFreshStore(t)
	address := site.address("/dup")

	summary := runFetchOver(t, st, []string{address, address, address})
	if want := (fetch.RunSummary{Listed: 1, Held: 1}); summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	if got := site.requestCount(); got != 1 {
		t.Fatalf("%d requests for one distinct address", got)
	}
	if pages, _ := st.Pages(); len(pages) != 1 {
		t.Fatalf("%d rows for one distinct address", len(pages))
	}
}

func TestRunPicksUpWhatAnInterruptedRunLeftPending(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		serveHTML(w, "<p>"+r.URL.Path+"</p>")
	})
	st, dir := openFreshStore(t)
	addresses := site.addresses("/crashed-after-writing", "/tried-once", "/never-started")
	if err := st.AddPending(addresses); err != nil {
		t.Fatal(err)
	}
	// The crash that the write order allows: the body is in place and the row is not yet ok.
	orphan, err := st.PutBody([]byte("<p>/crashed-after-writing</p>"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.NoteAttempt(addresses[1], store.Outcome{HTTPStatus: 500, Err: "HTTP 500 Internal Server Error"}); err != nil {
		t.Fatal(err)
	}

	summary := runFetchOver(t, st, addresses)
	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	if got := site.requestCount(); got != 3 {
		t.Fatalf("%d requests, want all three pending addresses fetched", got)
	}
	if page := rowFor(t, st, addresses[0]); page.SHA256 != orphan.SHA256 || page.Attempts != 1 {
		t.Errorf("row for the orphaned body = %+v", page)
	}
	if page := rowFor(t, st, addresses[1]); page.Attempts != 2 || page.State != store.StateOK {
		t.Errorf("row that had one attempt before = %+v, want ok with two in all", page)
	}
	if files := bodyFilesUnder(t, dir); len(files) != 3 {
		t.Errorf("%d body files for 3 distinct bodies: %v", len(files), files)
	}
}
