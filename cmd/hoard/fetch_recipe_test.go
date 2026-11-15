package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recipeSite serves a sitemap of the given paths and a page for each, except the paths
// under /gone/, which answer 404. It keeps the Cookie headers each path was asked with.
type recipeSite struct {
	*cliSite
	mu      sync.Mutex
	cookies map[string][][]string
}

func newRecipeSite(t *testing.T, pageDelay time.Duration, paths ...string) *recipeSite {
	t.Helper()
	site := &recipeSite{cookies: map[string][][]string{}}
	site.cliSite = newCLISite(t, func(w http.ResponseWriter, r *http.Request) {
		site.mu.Lock()
		site.cookies[r.URL.Path] = append(site.cookies[r.URL.Path], r.Header.Values("Cookie"))
		site.mu.Unlock()
		switch {
		case r.URL.Path == "/sitemap.xml":
			addresses := make([]string, len(paths))
			for i, path := range paths {
				addresses[i] = site.URL + path
			}
			fmt.Fprint(w, sitemapOf(addresses...))
		case strings.HasPrefix(r.URL.Path, "/gone/"):
			http.NotFound(w, r)
		default:
			time.Sleep(pageDelay)
			syntheticPageServer(w, r)
		}
	})
	return site
}

func (s *recipeSite) recipe(t *testing.T, fetchLines ...string) string {
	t.Helper()
	return writeRecipeFile(t, s.URL, fmt.Sprintf("kind = \"sitemap\"\nurl = %q", s.URL+"/sitemap.xml"), fetchLines...)
}

func (s *recipeSite) cookiesSeenOn(path string) [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.cookies[path]...)
}

// discovered runs hoard discover for the site into a fresh store and returns the store.
func (s *recipeSite) discovered(t *testing.T, recipePath string) string {
	t.Helper()
	storeDir := filepath.Join(t.TempDir(), "store")
	if run := runHoardBinary(t, "discover", "--recipe", recipePath, "--store", storeDir); run.exitCode != 0 {
		t.Fatalf("discover: exit code %d, stderr: %s", run.exitCode, run.stderr)
	}
	return storeDir
}

func TestFetchByRecipeFetchesWhatWasDiscoveredAtTheRecipesPace(t *testing.T) {
	site := newRecipeSite(t, 0, "/a", "/b", "/c")
	recipePath := site.recipe(t, `user_agent = "recipe-agent/2"`)
	storeDir := site.discovered(t, recipePath)

	run := runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", recipePath)
	if run.exitCode != 0 {
		t.Fatalf("exit code %d, stderr: %s", run.exitCode, run.stderr)
	}
	if got := strings.TrimSpace(run.stdout); got != "3 listed, 3 held, 0 failed, 0 pending" {
		t.Errorf("stdout = %q", got)
	}
	site.mu.Lock()
	agents := append([]string(nil), site.agents...)
	site.mu.Unlock()
	if len(agents) != 4 {
		t.Fatalf("the site saw %d requests, want the sitemap and 3 pages", len(agents))
	}
	for _, agent := range agents {
		if agent != "recipe-agent/2" {
			t.Errorf("a request arrived as %q, want the recipe's user agent", agent)
		}
	}

	again := runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", recipePath)
	if again.exitCode != 0 || strings.TrimSpace(again.stdout) != "3 listed, 3 held, 0 failed, 0 pending" {
		t.Errorf("second run: exit code %d, stdout %q", again.exitCode, again.stdout)
	}
	for _, path := range []string{"/a", "/b", "/c"} {
		if hits := site.hitsOn(path); hits != 1 {
			t.Errorf("%s was fetched %d times over two runs, want once", path, hits)
		}
	}
}

func TestFetchByRecipeAnswersForTheWholeLedger(t *testing.T) {
	site := newRecipeSite(t, 0, "/a", "/gone/b", "/c")
	recipePath := site.recipe(t)
	storeDir := site.discovered(t, recipePath)

	for attempt := 1; attempt <= 2; attempt++ {
		run := runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", recipePath)
		if run.exitCode != 1 {
			t.Errorf("run %d: exit code %d, want 1 for a capture with a failed address", attempt, run.exitCode)
		}
		if got := strings.TrimSpace(run.stdout); got != "3 listed, 2 held, 1 failed, 0 pending" {
			t.Errorf("run %d: stdout = %q", attempt, got)
		}
	}
	if hits := site.hitsOn("/gone/b"); hits != 1 {
		t.Errorf("the failed address was asked %d times over two runs, want once", hits)
	}
}

func TestFetchByRecipeUsesTheRecipesWorkers(t *testing.T) {
	site := newRecipeSite(t, 60*time.Millisecond, "/a", "/b", "/c", "/d", "/e", "/f")
	recipePath := site.recipe(t, "workers = 1")
	storeDir := site.discovered(t, recipePath)
	if run := runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", recipePath); run.exitCode != 0 {
		t.Fatalf("exit code %d, stderr: %s", run.exitCode, run.stderr)
	}
	if peak := site.peakInFlight(); peak != 1 {
		t.Errorf("%d requests were in flight at once, and the recipe says 1 worker", peak)
	}
}

func TestFetchByRecipeUsesTheRecipesRate(t *testing.T) {
	site := newRecipeSite(t, 0, "/a", "/b", "/c", "/d")
	// The sitemap and four pages at 5 a second are four gaps of 200ms. Only a lower bound
	// is asserted: a loaded machine makes a run slower, never faster than its pace.
	recipePath := site.recipe(t, "rate = 5.0")
	storeDir := site.discovered(t, recipePath)
	if run := runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", recipePath); run.exitCode != 0 {
		t.Fatalf("exit code %d, stderr: %s", run.exitCode, run.stderr)
	}
	site.mu.Lock()
	pages := append([]time.Time(nil), site.arrivals[1:]...)
	site.mu.Unlock()
	if len(pages) != 4 {
		t.Fatalf("the site saw %d page requests, want 4", len(pages))
	}
	if span := pages[3].Sub(pages[0]); span < 500*time.Millisecond {
		t.Errorf("4 pages arrived within %v; at the recipe's 5 a second they take at least 600ms", span)
	}
}

func TestFetchByRecipeUsesTheRecipesTimeout(t *testing.T) {
	// Each page takes far longer than the recipe allows and far less than the default
	// allows, so only the recipe's timeout can make the run give up on it.
	site := newRecipeSite(t, 1500*time.Millisecond, "/slow")
	recipePath := site.recipe(t, `timeout = "100ms"`)
	storeDir := site.discovered(t, recipePath)
	run := runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", recipePath)
	if run.exitCode != 1 {
		t.Errorf("exit code %d, want 1 for a page that outlasts the recipe's timeout", run.exitCode)
	}
	if got := strings.TrimSpace(run.stdout); got != "1 listed, 0 held, 1 failed, 0 pending" {
		t.Errorf("stdout = %q", got)
	}
}

func TestFetchByRecipeWithNothingDiscoveredSaysSo(t *testing.T) {
	site := newRecipeSite(t, 0, "/a")
	storeDir := filepath.Join(t.TempDir(), "store")
	run := runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", site.recipe(t))
	if run.exitCode != 1 {
		t.Errorf("exit code %d, want 1", run.exitCode)
	}
	if !strings.Contains(run.stderr, "hoard discover") {
		t.Errorf("stderr does not name the command that fills the ledger: %s", run.stderr)
	}
	if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
		t.Errorf("a run with nothing discovered left a store behind (stat error: %v)", err)
	}
	if hits := site.hitsOn("/a") + site.hitsOn("/sitemap.xml"); hits != 0 {
		t.Errorf("the site was asked %d times by a run with nothing to fetch", hits)
	}
}

func TestFetchExitsTwoWhenListAndRecipeAreMixedUp(t *testing.T) {
	site := newRecipeSite(t, 0, "/a")
	recipePath := site.recipe(t)
	storeDir := site.discovered(t, recipePath)
	list := writeListFile(t, site.URL+"/a")
	withRecipe := func(extra ...string) []string {
		return append([]string{"fetch", "--store", storeDir, "--recipe", recipePath}, extra...)
	}
	cases := []struct {
		name    string
		args    []string
		mention string
	}{
		{"both a list and a recipe", withRecipe("--list", list), "--list and --recipe cannot be combined"},
		{"workers with a recipe", withRecipe("--workers", "2"), "--workers cannot be combined with --recipe"},
		{"rate with a recipe", withRecipe("--rate", "5"), "--rate cannot be combined with --recipe"},
		{"timeout with a recipe", withRecipe("--timeout", "10s"), "--timeout cannot be combined with --recipe"},
		{"user agent with a recipe", withRecipe("--user-agent", "x"), "--user-agent cannot be combined with --recipe"},
		{"a recipe that does not exist", []string{"fetch", "--store", storeDir, "--recipe", filepath.Join(t.TempDir(), "absent.toml")}, "absent.toml"},
		{"a recipe that is invalid", []string{"fetch", "--store", storeDir, "--recipe", writeRecipeFile(t, site.URL, "kind = \"crawl\"")}, "discover.kind"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := runHoardBinary(t, c.args...)
			if run.exitCode != 2 {
				t.Errorf("exit code %d, want 2; stderr: %s", run.exitCode, run.stderr)
			}
			if !strings.Contains(run.stderr, c.mention) {
				t.Errorf("stderr does not say %q:\n%s", c.mention, run.stderr)
			}
		})
	}
	if hits := site.hitsOn("/a"); hits != 0 {
		t.Errorf("a page was fetched %d times by command lines that could not be read", hits)
	}
}

const recipeCookieUnderTest = "session=r3cipe-s3cret-41c9; plan=paid"

func writeCookieFileWithMode(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "site.cookie")
	if err := os.WriteFile(path, []byte(recipeCookieUnderTest+"\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFetchByRecipeSignsItsRequestsWithTheRecipesCookie(t *testing.T) {
	site := newRecipeSite(t, 0, "/a", "/b")
	recipePath := site.recipe(t, fmt.Sprintf("cookie_file = %q", writeCookieFileWithMode(t, 0o600)))
	storeDir := site.discovered(t, recipePath)
	if run := runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", recipePath); run.exitCode != 0 {
		t.Fatalf("exit code %d, stderr: %s", run.exitCode, run.stderr)
	}
	for _, path := range []string{"/sitemap.xml", "/a", "/b"} {
		seen := site.cookiesSeenOn(path)
		if len(seen) != 1 {
			t.Errorf("%s was asked %d times, want once", path, len(seen))
			continue
		}
		if len(seen[0]) != 1 || seen[0][0] != recipeCookieUnderTest {
			t.Errorf("%s was asked with Cookie %q, want exactly the file's value", path, seen[0])
		}
	}
}

func TestFetchByRecipeStopsBeforeAnyRequestWhenTheCookieCannotBeUsed(t *testing.T) {
	site := newRecipeSite(t, 0, "/a", "/b")
	storeDir := site.discovered(t, site.recipe(t))
	cases := []struct {
		name       string
		cookieFile string
	}{
		{"the file is missing", filepath.Join(t.TempDir(), "absent.cookie")},
		{"the file is readable by others", writeCookieFileWithMode(t, 0o644)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recipePath := site.recipe(t, fmt.Sprintf("cookie_file = %q", c.cookieFile))
			run := runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", recipePath)
			if run.exitCode != 1 {
				t.Errorf("exit code %d, want 1; stderr: %s", run.exitCode, run.stderr)
			}
			if !strings.Contains(run.stderr, "cookie file") {
				t.Errorf("stderr does not name the cookie file as the cause: %s", run.stderr)
			}
			if strings.Contains(run.stderr+run.stdout, "r3cipe-s3cret") {
				t.Errorf("the output quotes the cookie:\n%s%s", run.stdout, run.stderr)
			}
		})
	}
	if hits := site.hitsOn("/a") + site.hitsOn("/b"); hits != 0 {
		t.Errorf("%d pages were fetched signed out by runs that were meant to be signed in", hits)
	}
}
