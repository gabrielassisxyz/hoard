package discover

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/recipe"
)

// seenRequest is what a synthetic server remembers of one request.
type seenRequest struct {
	path, cookie, userAgent string
	hasCookie               bool
}

// syntheticSite is a local server standing in for a web site. Its pages are set per test.
type syntheticSite struct {
	*httptest.Server
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	seen     []seenRequest
}

func newSyntheticSite(t *testing.T) *syntheticSite {
	t.Helper()
	site := &syntheticSite{handlers: map[string]http.HandlerFunc{}}
	site.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasCookie := r.Header["Cookie"]
		site.mu.Lock()
		site.seen = append(site.seen, seenRequest{r.URL.Path, r.Header.Get("Cookie"), r.Header.Get("User-Agent"), hasCookie})
		handler := site.handlers[r.URL.Path]
		site.mu.Unlock()
		if handler == nil {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(site.Close)
	return site
}

func (s *syntheticSite) serve(path, contentType, body string) {
	s.handle(path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		fmt.Fprint(w, body)
	})
}

func (s *syntheticSite) handle(path string, handler http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[path] = handler
}

func (s *syntheticSite) requests() []seenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seenRequest(nil), s.seen...)
}

func (s *syntheticSite) host() string {
	return strings.TrimPrefix(s.URL, "http://")
}

// recipeFor loads a real recipe file for the site, so these tests read what a person
// would write. The rate is high only to keep the tests short.
func recipeFor(t *testing.T, site *syntheticSite, discoverTable string, fetchLines ...string) recipe.Recipe {
	t.Helper()
	content := fmt.Sprintf("[site]\nname = \"synthetic\"\nhost = %q\n\n[discover]\n%s\n\n[fetch]\nrate = 1000.0\n%s\n",
		site.host(), discoverTable, strings.Join(fetchLines, "\n"))
	path := filepath.Join(t.TempDir(), "recipe.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := recipe.Load(path)
	if err != nil {
		t.Fatalf("the test's own recipe does not load: %v\n%s", err, content)
	}
	return loaded
}

func sitemapRecipe(t *testing.T, site *syntheticSite, fetchLines ...string) recipe.Recipe {
	t.Helper()
	return recipeFor(t, site, fmt.Sprintf("kind = \"sitemap\"\nurl = %q", site.URL+"/sitemap.xml"), fetchLines...)
}

func urlset(addresses ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, address := range addresses {
		fmt.Fprintf(&b, "<url><loc> %s </loc><lastmod>2026-01-01</lastmod></url>", address)
	}
	b.WriteString("</urlset>")
	return b.String()
}

func sitemapIndex(children ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, child := range children {
		fmt.Fprintf(&b, "<sitemap><loc>%s</loc></sitemap>", child)
	}
	b.WriteString("</sitemapindex>")
	return b.String()
}

func mustDiscover(t *testing.T, r recipe.Recipe) Result {
	t.Helper()
	result, err := Run(context.Background(), r)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result
}

func wantAddresses(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("addresses:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestSitemapOfPagesGivesEveryLocation(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/sitemap.xml", "application/xml", urlset(site.URL+"/a", site.URL+"/b", site.URL+"/c"))
	result := mustDiscover(t, sitemapRecipe(t, site))
	wantAddresses(t, result.Addresses, site.URL+"/a", site.URL+"/b", site.URL+"/c")
	if result.OffSite != 0 {
		t.Errorf("OffSite = %d, want 0", result.OffSite)
	}
}

func TestSitemapIndexIsReadOneLevelDeep(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/sitemap.xml", "application/xml", sitemapIndex(site.URL+"/posts.xml", site.URL+"/notes.xml"))
	site.serve("/posts.xml", "application/xml", urlset(site.URL+"/p/1", site.URL+"/p/2"))
	site.serve("/notes.xml", "application/xml", urlset(site.URL+"/n/1"))
	result := mustDiscover(t, sitemapRecipe(t, site))
	wantAddresses(t, result.Addresses, site.URL+"/p/1", site.URL+"/p/2", site.URL+"/n/1")
}

func TestSitemapThatCannotBeTrustedFailsTheDiscovery(t *testing.T) {
	other := newSyntheticSite(t)
	other.serve("/elsewhere.xml", "application/xml", urlset(other.URL+"/x"))
	cases := []struct {
		name   string
		build  func(site *syntheticSite)
		within string
	}{
		{"an index inside an index", func(site *syntheticSite) {
			site.serve("/sitemap.xml", "application/xml", sitemapIndex(site.URL+"/deeper.xml"))
			site.serve("/deeper.xml", "application/xml", sitemapIndex(site.URL+"/deepest.xml"))
		}, "one level deep"},
		{"a child on another host", func(site *syntheticSite) {
			site.serve("/sitemap.xml", "application/xml", sitemapIndex(other.URL+"/elsewhere.xml"))
		}, "not on the site"},
		{"a document that is not a sitemap", func(site *syntheticSite) {
			site.serve("/sitemap.xml", "text/html", "<html><body>Sign in</body></html>")
		}, "not a sitemap"},
		{"a second child that is missing", func(site *syntheticSite) {
			site.serve("/sitemap.xml", "application/xml", sitemapIndex(site.URL+"/posts.xml", site.URL+"/gone.xml"))
			site.serve("/posts.xml", "application/xml", urlset(site.URL+"/p/1"))
		}, "404"},
		{"a second child the server fails on", func(site *syntheticSite) {
			site.serve("/sitemap.xml", "application/xml", sitemapIndex(site.URL+"/posts.xml", site.URL+"/broken.xml"))
			site.serve("/posts.xml", "application/xml", urlset(site.URL+"/p/1"))
			site.handle("/broken.xml", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
		}, "500"},
		{"a refusal", func(site *syntheticSite) {
			site.handle("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
		}, "403"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			site := newSyntheticSite(t)
			c.build(site)
			result, err := Run(context.Background(), sitemapRecipe(t, site))
			if err == nil {
				t.Fatalf("Run returned %d addresses and no error", len(result.Addresses))
			}
			if len(result.Addresses) != 0 {
				t.Errorf("Run returned %d addresses alongside its error; a failed discovery has no list", len(result.Addresses))
			}
			if !strings.Contains(err.Error(), c.within) {
				t.Errorf("error %q does not say %q", err, c.within)
			}
		})
	}
	if len(other.requests()) != 0 {
		t.Errorf("the other host was asked %d times for a child sitemap", len(other.requests()))
	}
}

func TestGzippedSitemapReadsLikeThePlainOne(t *testing.T) {
	site := newSyntheticSite(t)
	plain := urlset(site.URL+"/a", site.URL+"/b")
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	writer.Write([]byte(plain))
	writer.Close()
	site.handle("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-gzip")
		w.Write(compressed.Bytes())
	})
	wantAddresses(t, mustDiscover(t, sitemapRecipe(t, site)).Addresses, site.URL+"/a", site.URL+"/b")
}

func TestSitemapPatternKeepsOnlyWhatItMatches(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/sitemap.xml", "application/xml", urlset(site.URL+"/p/1", site.URL+"/about", site.URL+"/p/2"))
	r := recipeFor(t, site, fmt.Sprintf("kind = \"sitemap\"\nurl = %q\npattern = \"/p/\"", site.URL+"/sitemap.xml"))
	wantAddresses(t, mustDiscover(t, r).Addresses, site.URL+"/p/1", site.URL+"/p/2")
}

const indexPage = `<!doctype html><html><head><title>Essays</title></head><body>
<a href="one.html">One</a>
<a href="/two.html#notes">Two, at its notes</a>
<a href="/two.html">Two</a>
<a href="two.html#top">Two again</a>
<a href="about.txt">About</a>
<a href="mailto:someone@example.invalid">Write</a>
<a name="anchor-without-href">nothing</a>
<A HREF="three.html">Three</A>
</body></html>`

func linksRecipe(t *testing.T, site *syntheticSite, page string) recipe.Recipe {
	t.Helper()
	return recipeFor(t, site, fmt.Sprintf("kind = \"links\"\nurl = %q\npattern = \"\\\\.html$\"", site.URL+page))
}

func TestLinksKeepsThePatternAndDropsFragments(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/articles.html", "text/html", indexPage)
	result := mustDiscover(t, linksRecipe(t, site, "/articles.html"))
	wantAddresses(t, result.Addresses, site.URL+"/one.html", site.URL+"/two.html", site.URL+"/three.html")
}

// The pattern here matches a fragment too, so only dropping the fragment makes these one.
func TestLinksThatDifferOnlyByFragmentAreOnePage(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/articles.html", "text/html", `<a href="/two.html#notes">Notes</a><a href="/two.html">Two</a><a href="two.html#top">Top</a>`)
	r := recipeFor(t, site, fmt.Sprintf("kind = \"links\"\nurl = %q\npattern = \"two\"", site.URL+"/articles.html"))
	wantAddresses(t, mustDiscover(t, r).Addresses, site.URL+"/two.html")
}

func TestLinksResolveAgainstWhereThePageWasServedFrom(t *testing.T) {
	site := newSyntheticSite(t)
	site.handle("/articles.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/essays/index.html", http.StatusFound)
	})
	site.serve("/essays/index.html", "text/html", `<a href="one.html">One</a>`)
	wantAddresses(t, mustDiscover(t, linksRecipe(t, site, "/articles.html")).Addresses, site.URL+"/essays/one.html")
}

func TestLinksResolveAgainstTheBaseElement(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/articles.html", "text/html", `<html><head><base href="/library/"><base href="/ignored/"></head><body><a href="one.html">One</a></body></html>`)
	wantAddresses(t, mustDiscover(t, linksRecipe(t, site, "/articles.html")).Addresses, site.URL+"/library/one.html")
}

func TestAddressesOnAnotherHostAreCountedAndLeftOut(t *testing.T) {
	other := newSyntheticSite(t)
	t.Run("sitemap", func(t *testing.T) {
		site := newSyntheticSite(t)
		site.serve("/sitemap.xml", "application/xml", urlset(site.URL+"/a", other.URL+"/b", "https://elsewhere.invalid/c"))
		result := mustDiscover(t, sitemapRecipe(t, site))
		wantAddresses(t, result.Addresses, site.URL+"/a")
		if result.OffSite != 2 {
			t.Errorf("OffSite = %d, want 2", result.OffSite)
		}
	})
	t.Run("links", func(t *testing.T) {
		site := newSyntheticSite(t)
		site.serve("/articles.html", "text/html", fmt.Sprintf(`<a href="one.html">One</a><a href="%s/far.html">Far</a>`, other.URL))
		result := mustDiscover(t, linksRecipe(t, site, "/articles.html"))
		wantAddresses(t, result.Addresses, site.URL+"/one.html")
		if result.OffSite != 1 {
			t.Errorf("OffSite = %d, want 1", result.OffSite)
		}
	})
	t.Run("command", func(t *testing.T) {
		site := newSyntheticSite(t)
		r := commandRecipe(t, site, "print-file", site.URL+"/a\n"+other.URL+"/b\n")
		result := mustDiscover(t, r)
		wantAddresses(t, result.Addresses, site.URL+"/a")
		if result.OffSite != 1 {
			t.Errorf("OffSite = %d, want 1", result.OffSite)
		}
	})
}

func TestADiscoveryThatFindsNothingIsAnError(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/sitemap.xml", "application/xml", urlset())
	if result, err := Run(context.Background(), sitemapRecipe(t, site)); err == nil {
		t.Errorf("Run returned %d addresses and no error for an empty sitemap", len(result.Addresses))
	}
	site.serve("/sitemap.xml", "application/xml", urlset("https://elsewhere.invalid/only"))
	if _, err := Run(context.Background(), sitemapRecipe(t, site)); err == nil {
		t.Error("Run returned no error when every address was on another host")
	}
}

func TestAnAddressListedTwiceIsFoundOnce(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/sitemap.xml", "application/xml", urlset(site.URL+"/a", site.URL+"/b", site.URL+"/a"))
	wantAddresses(t, mustDiscover(t, sitemapRecipe(t, site)).Addresses, site.URL+"/a", site.URL+"/b")
}

func TestASitemapEntryThatIsNotAnAddressFailsTheDiscovery(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/sitemap.xml", "application/xml", urlset(site.URL+"/a", "/relative/only"))
	if _, err := Run(context.Background(), sitemapRecipe(t, site)); err == nil || !strings.Contains(err.Error(), "/relative/only") {
		t.Errorf("Run error = %v, want one naming the entry that is not an address", err)
	}
}

func TestRequestsCarryTheRecipesUserAgent(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/sitemap.xml", "application/xml", urlset(site.URL+"/a"))
	mustDiscover(t, sitemapRecipe(t, site, `user_agent = "recipe-agent/1"`))
	for _, request := range site.requests() {
		if request.userAgent != "recipe-agent/1" {
			t.Errorf("%s was asked as %q, want the recipe's user agent", request.path, request.userAgent)
		}
	}
}

// writeCookieFile leaves a cookie where a recipe can name it, private to its owner.
func writeCookieFile(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "site.cookie")
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheCookieGoesToTheSiteAndNowhereElse(t *testing.T) {
	const cookie = "session=only-for-the-site"
	site, other := newSyntheticSite(t), newSyntheticSite(t)
	// The site hands its sitemap over to another host, as a site that keeps it on a CDN does.
	site.handle("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/hosted-sitemap.xml", http.StatusFound)
	})
	other.serve("/hosted-sitemap.xml", "application/xml", sitemapIndex(site.URL+"/posts.xml"))
	site.serve("/posts.xml", "application/xml", urlset(site.URL+"/p/1"))
	r := sitemapRecipe(t, site, fmt.Sprintf("cookie_file = %q", writeCookieFile(t, cookie)))
	wantAddresses(t, mustDiscover(t, r).Addresses, site.URL+"/p/1")

	if len(site.requests()) != 2 {
		t.Fatalf("the site saw %d requests, want 2", len(site.requests()))
	}
	for _, request := range site.requests() {
		if request.cookie != cookie {
			t.Errorf("the site's %s arrived with cookie %q, want the recipe's", request.path, request.cookie)
		}
	}
	if len(other.requests()) != 1 {
		t.Fatalf("the other host saw %d requests, want the 1 it was redirected", len(other.requests()))
	}
	for _, request := range other.requests() {
		if request.hasCookie {
			t.Errorf("the other host's %s arrived with a Cookie header", request.path)
		}
	}
}

func TestNoCookieIsSentWhenTheRecipeNamesNone(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/sitemap.xml", "application/xml", urlset(site.URL+"/a"))
	mustDiscover(t, sitemapRecipe(t, site))
	for _, request := range site.requests() {
		if request.hasCookie {
			t.Errorf("%s arrived with a Cookie header and the recipe names no cookie file", request.path)
		}
	}
}

func TestACookieFileThatCannotBeUsedStopsBeforeAnyRequest(t *testing.T) {
	site := newSyntheticSite(t)
	site.serve("/sitemap.xml", "application/xml", urlset(site.URL+"/a"))
	r := sitemapRecipe(t, site, fmt.Sprintf("cookie_file = %q", filepath.Join(t.TempDir(), "absent.cookie")))
	if _, err := Run(context.Background(), r); err == nil {
		t.Error("Run returned no error with a cookie file that does not exist")
	}
	if len(site.requests()) != 0 {
		t.Errorf("the site was asked %d times before the cookie file was found missing", len(site.requests()))
	}
}

const helperModeVariable = "HOARD_DISCOVER_TEST_HELPER"

// TestHelperProcess is not a test. Run with the variable set, this test binary plays a
// site's discover command, so the command kind is exercised with a real process.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv(helperModeVariable) {
	case "":
		return
	case "print-file":
		// A relative path: this only works when the command runs in the recipe's directory.
		content, err := os.ReadFile("addresses.txt")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Stdout.Write(content)
		os.Exit(0)
	case "print-file-then-fail":
		// What it prints is a perfectly good list, so only the exit code can fail it.
		content, _ := os.ReadFile("addresses.txt")
		os.Stdout.Write(content)
		os.Exit(7)
	case "hang":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(4)
}

// commandRecipe is a recipe whose command is this test binary in one of its helper modes.
// output, when not empty, is what the helper finds to print in the recipe's directory.
func commandRecipe(t *testing.T, site *syntheticSite, mode, output string) recipe.Recipe {
	t.Helper()
	t.Setenv(helperModeVariable, mode)
	r := recipeFor(t, site, fmt.Sprintf("kind = \"command\"\ncommand = [%q, \"-test.run=^TestHelperProcess$\"]", os.Args[0]))
	if output != "" {
		if err := os.WriteFile(filepath.Join(r.Dir, "addresses.txt"), []byte(output), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestCommandOutputBecomesTheList(t *testing.T) {
	site := newSyntheticSite(t)
	r := commandRecipe(t, site, "print-file", "# a comment\n"+site.URL+"/a\n\n  "+site.URL+"/b  \n")
	wantAddresses(t, mustDiscover(t, r).Addresses, site.URL+"/a", site.URL+"/b")
	if len(site.requests()) != 0 {
		t.Errorf("the site was asked %d times; a command recipe makes no request of its own", len(site.requests()))
	}
}

func TestCommandThatFailsGivesNoList(t *testing.T) {
	site := newSyntheticSite(t)
	result, err := Run(context.Background(), commandRecipe(t, site, "print-file-then-fail", site.URL+"/a\n"))
	if err == nil {
		t.Fatalf("Run returned %v and no error for a command that exited 7", result.Addresses)
	}
	if !strings.Contains(err.Error(), "exit status 7") {
		t.Errorf("error %q does not carry the command's exit status", err)
	}
	if len(result.Addresses) != 0 {
		t.Errorf("Run kept %v from a command that failed", result.Addresses)
	}
}

func TestCommandThatDoesNotFinishIsStopped(t *testing.T) {
	site := newSyntheticSite(t)
	previous := commandTimeout
	commandTimeout = 300 * time.Millisecond
	t.Cleanup(func() { commandTimeout = previous })
	_, err := Run(context.Background(), commandRecipe(t, site, "hang", ""))
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("Run error = %v, want one saying the command did not finish", err)
	}
}

func TestLinksOfIgnoresWhatIsNotAWebAddress(t *testing.T) {
	servedFrom, _ := url.Parse("https://site.invalid/dir/index.html")
	got := linksOf([]byte(`<a href="javascript:void(0)">x</a><a href="tel:123">y</a><a href="">self</a><a href="?page=2">next</a>`), servedFrom)
	wantAddresses(t, got, "https://site.invalid/dir/index.html", "https://site.invalid/dir/index.html?page=2")
}
