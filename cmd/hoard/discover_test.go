package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gabrielassisxyz/hoard/internal/store"
)

// writeRecipeFile writes a recipe for a synthetic site. Unless a test states a rate of its
// own, the rate is high only to keep the tests short.
func writeRecipeFile(t *testing.T, siteURL, discoverTable string, fetchLines ...string) string {
	t.Helper()
	if !slices.ContainsFunc(fetchLines, func(line string) bool { return strings.HasPrefix(line, "rate") }) {
		fetchLines = append(fetchLines, "rate = 1000.0")
	}
	content := fmt.Sprintf("[site]\nname = \"synthetic\"\nhost = %q\n\n[discover]\n%s\n\n[fetch]\n%s\n",
		strings.TrimPrefix(siteURL, "http://"), discoverTable, strings.Join(fetchLines, "\n"))
	path := filepath.Join(t.TempDir(), "recipe.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func sitemapOf(addresses ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, address := range addresses {
		fmt.Fprintf(&b, "<url><loc>%s</loc></url>", address)
	}
	b.WriteString("</urlset>")
	return b.String()
}

// ledgerStates reads the store the way any later command would and returns each address's
// state.
func ledgerStates(t *testing.T, storeDir string) map[string]string {
	t.Helper()
	st, err := store.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pages, err := st.Pages()
	if err != nil {
		t.Fatal(err)
	}
	states := make(map[string]string, len(pages))
	for _, page := range pages {
		states[page.Address] = page.State
	}
	return states
}

// sitemapSite serves a sitemap of three pages, and the pages themselves. failSitemap makes
// the sitemap answer 500 from then on.
type sitemapSite struct {
	*cliSite
	failSitemap atomic.Bool
}

func newSitemapSite(t *testing.T) *sitemapSite {
	t.Helper()
	site := &sitemapSite{}
	site.cliSite = newCLISite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sitemap.xml" {
			syntheticPageServer(w, r)
			return
		}
		if site.failSitemap.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, sitemapOf(site.URL+"/a", site.URL+"/b", site.URL+"/c"))
	})
	return site
}

func (s *sitemapSite) recipe(t *testing.T) string {
	t.Helper()
	return writeRecipeFile(t, s.URL, fmt.Sprintf("kind = \"sitemap\"\nurl = %q", s.URL+"/sitemap.xml"))
}

func TestDiscoverRecordsWhatItFindsAsPending(t *testing.T) {
	site := newSitemapSite(t)
	storeDir := filepath.Join(t.TempDir(), "store")
	run := runHoardBinary(t, "discover", "--recipe", site.recipe(t), "--store", storeDir)
	if run.exitCode != 0 {
		t.Fatalf("exit code %d, stderr: %s", run.exitCode, run.stderr)
	}
	if got := strings.TrimSpace(run.stdout); got != "3 found, 3 new, 0 off-site" {
		t.Errorf("stdout = %q, want %q", got, "3 found, 3 new, 0 off-site")
	}
	states := ledgerStates(t, storeDir)
	for _, path := range []string{"/a", "/b", "/c"} {
		if states[site.URL+path] != store.StatePending {
			t.Errorf("%s is %q in the ledger, want pending", path, states[site.URL+path])
		}
	}
	if len(states) != 3 {
		t.Errorf("the ledger has %d rows, want 3", len(states))
	}
	if site.hitsOn("/a") != 0 {
		t.Errorf("discover fetched a page; it only lists them")
	}
}

func TestDiscoverAgainAddsNothingAndReopensNothing(t *testing.T) {
	site := newSitemapSite(t)
	storeDir := filepath.Join(t.TempDir(), "store")
	recipePath := site.recipe(t)
	if run := runHoardBinary(t, "discover", "--recipe", recipePath, "--store", storeDir); run.exitCode != 0 {
		t.Fatalf("first discover: exit code %d, stderr: %s", run.exitCode, run.stderr)
	}
	// Settle two of the three, so the second discovery meets rows in every kind of state.
	list := writeListFile(t, site.URL+"/a", site.URL+"/b")
	if run := runHoardBinary(t, "fetch", "--store", storeDir, "--list", list, "--rate", "1000"); run.exitCode != 0 {
		t.Fatalf("fetch: exit code %d, stderr: %s", run.exitCode, run.stderr)
	}
	before := ledgerStates(t, storeDir)

	run := runHoardBinary(t, "discover", "--recipe", recipePath, "--store", storeDir)
	if run.exitCode != 0 {
		t.Fatalf("second discover: exit code %d, stderr: %s", run.exitCode, run.stderr)
	}
	if got := strings.TrimSpace(run.stdout); got != "3 found, 0 new, 0 off-site" {
		t.Errorf("stdout = %q, want %q", got, "3 found, 0 new, 0 off-site")
	}
	after := ledgerStates(t, storeDir)
	if before[site.URL+"/a"] != store.StateOK || after[site.URL+"/a"] != store.StateOK {
		t.Errorf("/a went from %q to %q, want ok kept", before[site.URL+"/a"], after[site.URL+"/a"])
	}
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Errorf("the second discovery changed the ledger:\n before %v\n after  %v", before, after)
	}
}

func TestDiscoverThatFailsLeavesTheLedgerAsItWas(t *testing.T) {
	site := newSitemapSite(t)
	recipePath := site.recipe(t)

	t.Run("no store is created", func(t *testing.T) {
		site.failSitemap.Store(true)
		defer site.failSitemap.Store(false)
		storeDir := filepath.Join(t.TempDir(), "store")
		run := runHoardBinary(t, "discover", "--recipe", recipePath, "--store", storeDir)
		if run.exitCode != 1 {
			t.Errorf("exit code %d, want 1; stderr: %s", run.exitCode, run.stderr)
		}
		if !strings.Contains(run.stderr, "500") {
			t.Errorf("stderr does not name the answer that failed it: %s", run.stderr)
		}
		if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
			t.Errorf("a failed discovery left a store behind (stat error: %v)", err)
		}
	})

	t.Run("an existing ledger keeps its rows", func(t *testing.T) {
		storeDir := filepath.Join(t.TempDir(), "store")
		if run := runHoardBinary(t, "discover", "--recipe", recipePath, "--store", storeDir); run.exitCode != 0 {
			t.Fatalf("first discover: exit code %d, stderr: %s", run.exitCode, run.stderr)
		}
		before := ledgerStates(t, storeDir)
		site.failSitemap.Store(true)
		defer site.failSitemap.Store(false)
		if run := runHoardBinary(t, "discover", "--recipe", recipePath, "--store", storeDir); run.exitCode != 1 {
			t.Errorf("exit code %d, want 1", run.exitCode)
		}
		if after := ledgerStates(t, storeDir); fmt.Sprint(before) != fmt.Sprint(after) {
			t.Errorf("the failed discovery changed the ledger:\n before %v\n after  %v", before, after)
		}
	})
}

func TestDiscoverExitsTwoWhenItCannotReadWhatItWasGiven(t *testing.T) {
	site := newSitemapSite(t)
	storeDir := filepath.Join(t.TempDir(), "store")
	invalid := writeRecipeFile(t, site.URL, "kind = \"crawl\"\ndepth = 3")
	cases := []struct {
		name   string
		args   []string
		stderr string
	}{
		{"no recipe", []string{"discover", "--store", storeDir}, "--recipe is required"},
		{"no store", []string{"discover", "--recipe", site.recipe(t)}, "--store is required"},
		{"extra arguments", []string{"discover", "--recipe", site.recipe(t), "--store", storeDir, "extra"}, "unexpected arguments"},
		{"a recipe that does not exist", []string{"discover", "--recipe", filepath.Join(t.TempDir(), "absent.toml"), "--store", storeDir}, "absent.toml"},
		{"a recipe with an unknown kind", []string{"discover", "--recipe", invalid, "--store", storeDir}, "discover.kind"},
		{"a recipe with an unknown key", []string{"discover", "--recipe", invalid, "--store", storeDir}, "discover.depth: unknown key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := runHoardBinary(t, c.args...)
			if run.exitCode != 2 {
				t.Errorf("exit code %d, want 2", run.exitCode)
			}
			if !strings.Contains(run.stderr, c.stderr) {
				t.Errorf("stderr does not say %q:\n%s", c.stderr, run.stderr)
			}
		})
	}
	if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
		t.Errorf("a command line that could not be read left a store behind (stat error: %v)", err)
	}
	if site.hitsOn("/sitemap.xml") != 0 {
		t.Errorf("the site was asked for its sitemap %d times by commands that could not be read", site.hitsOn("/sitemap.xml"))
	}
}

func TestTopLevelUsageNamesDiscover(t *testing.T) {
	run := runHoardBinary(t, "help")
	if !strings.Contains(run.stdout, "discover") {
		t.Errorf("the usage does not name discover:\n%s", run.stdout)
	}
}
