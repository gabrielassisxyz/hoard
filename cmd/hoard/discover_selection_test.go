package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabrielassisxyz/hoard/internal/store"
)

// selectionSitePaths are a synthetic site's pages in the order its sitemap lists them: three
// sections, mixed, and not alphabetical, so that "the first ones" is neither "the first
// section" nor "the first alphabetically". /people/ starts like /p/ and is not under it.
var selectionSitePaths = []string{
	"/people/zoe", "/essays/on-time", "/p/walnut", "/essays/on-rain", "/p/apple",
	"/people/yan", "/essays/on-salt", "/p/mango", "/essays/on-fire", "/people/xi",
}

// newSelectionSite serves a sitemap of the paths in the order given, with an address on
// another host before them and another after, and a page for every path. The page at
// /essays/on-salt is gone, so that a row of the ledger can be settled as failed.
func newSelectionSite(t *testing.T, paths ...string) *cliSite {
	t.Helper()
	return newCLISite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sitemap.xml":
			addresses := []string{"http://elsewhere.test/off-1"}
			for _, path := range paths {
				addresses = append(addresses, "http://"+r.Host+path)
			}
			addresses = append(addresses, "http://elsewhere.test/off-2")
			fmt.Fprint(w, sitemapOf(addresses...))
		case "/essays/on-salt":
			http.NotFound(w, r)
		default:
			syntheticPageServer(w, r)
		}
	})
}

func selectionRecipe(t *testing.T, site *cliSite, extraDiscoverLines ...string) string {
	t.Helper()
	table := fmt.Sprintf("kind = \"sitemap\"\nurl = %q", site.URL+"/sitemap.xml")
	if len(extraDiscoverLines) > 0 {
		table += "\n" + strings.Join(extraDiscoverLines, "\n")
	}
	return writeRecipeFile(t, site.URL, table)
}

func discoverWithSelection(t *testing.T, recipePath, storeDir string, flags ...string) hoardRun {
	t.Helper()
	return runHoardBinary(t, append([]string{"discover", "--recipe", recipePath, "--store", storeDir}, flags...)...)
}

// summaryOfDiscover runs the discovery, requires it to succeed, and returns the line it
// printed.
func summaryOfDiscover(t *testing.T, recipePath, storeDir string, flags ...string) string {
	t.Helper()
	run := discoverWithSelection(t, recipePath, storeDir, flags...)
	if run.exitCode != 0 {
		t.Fatalf("discover %v: exit code %d, stderr: %s", flags, run.exitCode, run.stderr)
	}
	return strings.TrimSpace(run.stdout)
}

// ledgerPathsOf is the path of every address the ledger has, sorted.
func ledgerPathsOf(t *testing.T, siteURL, storeDir string) []string {
	t.Helper()
	var paths []string
	for address := range ledgerStates(t, storeDir) {
		paths = append(paths, strings.TrimPrefix(address, siteURL))
	}
	slices.Sort(paths)
	return paths
}

func sortedCopyOf(paths []string) []string {
	sorted := slices.Clone(paths)
	slices.Sort(sorted)
	return sorted
}

func requireLedgerPaths(t *testing.T, siteURL, storeDir string, want []string) {
	t.Helper()
	if got := ledgerPathsOf(t, siteURL, storeDir); !slices.Equal(got, sortedCopyOf(want)) {
		t.Errorf("the ledger holds:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(sortedCopyOf(want), "\n  "))
	}
}

func requireSummary(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func selectionSitemapIndexOf(children ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, child := range children {
		fmt.Fprintf(&b, "<sitemap><loc>%s</loc></sitemap>", child)
	}
	b.WriteString("</sitemapindex>")
	return b.String()
}

func TestDiscoverPathsRecordsOnlyAddressesUnderThePrefixes(t *testing.T) {
	site := newSelectionSite(t, selectionSitePaths...)
	storeDir := filepath.Join(t.TempDir(), "store")
	line := summaryOfDiscover(t, selectionRecipe(t, site), storeDir, "--paths", "/p/,/essays/")
	requireSummary(t, line, "7 found, 7 new, 2 off-site")
	requireLedgerPaths(t, site.URL, storeDir, []string{
		"/p/walnut", "/p/apple", "/p/mango",
		"/essays/on-time", "/essays/on-rain", "/essays/on-salt", "/essays/on-fire",
	})
}

func TestDiscoverPathsAndThePatternBothHaveToPass(t *testing.T) {
	site := newSelectionSite(t, "/p/keep-1", "/p/skip-2", "/people/keep-3", "/people/skip-4")
	storeDir := filepath.Join(t.TempDir(), "store")
	recipePath := selectionRecipe(t, site, `pattern = "keep"`)
	summaryOfDiscover(t, recipePath, storeDir, "--paths", "/p/")
	requireLedgerPaths(t, site.URL, storeDir, []string{"/p/keep-1"})
}

func TestDiscoverLimitTakesTheFirstInTheSitesOrderAndAskingAgainAddsNothing(t *testing.T) {
	site := newSelectionSite(t, selectionSitePaths...)
	storeDir := filepath.Join(t.TempDir(), "store")
	recipePath := selectionRecipe(t, site)

	line := summaryOfDiscover(t, recipePath, storeDir, "--limit", "3")
	requireSummary(t, line, "10 found, 3 new, 2 off-site, 7 beyond the limit")
	requireLedgerPaths(t, site.URL, storeDir, selectionSitePaths[:3])

	// One of the three is settled, so that the next call meets a row that is not pending.
	list := writeListFile(t, site.URL+selectionSitePaths[0])
	if run := runHoardBinary(t, "fetch", "--store", storeDir, "--list", list, "--rate", "1000"); run.exitCode != 0 {
		t.Fatalf("fetch: exit code %d, stderr: %s", run.exitCode, run.stderr)
	}
	before := ledgerStates(t, storeDir)
	if before[site.URL+selectionSitePaths[0]] != store.StateOK {
		t.Fatalf("%s is %q, want ok", selectionSitePaths[0], before[site.URL+selectionSitePaths[0]])
	}

	line = summaryOfDiscover(t, recipePath, storeDir, "--limit", "3")
	requireSummary(t, line, "10 found, 0 new, 2 off-site, 7 beyond the limit")
	if after := ledgerStates(t, storeDir); fmt.Sprint(before) != fmt.Sprint(after) {
		t.Errorf("asking again changed the ledger:\n before %v\n after  %v", before, after)
	}

	line = summaryOfDiscover(t, recipePath, storeDir, "--limit", "5")
	requireSummary(t, line, "10 found, 2 new, 2 off-site, 5 beyond the limit")
	requireLedgerPaths(t, site.URL, storeDir, selectionSitePaths[:5])
}

// The order of an index is the order it names its children in, which is neither the order
// of their addresses nor the order of the addresses inside them.
func TestDiscoverLimitFollowsTheOrderAnIndexNamesItsChildren(t *testing.T) {
	site := newCLISite(t, func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/sitemap.xml":
			fmt.Fprint(w, selectionSitemapIndexOf(base+"/b-posts.xml", base+"/a-notes.xml"))
		case "/b-posts.xml":
			fmt.Fprint(w, sitemapOf(base+"/p/b2", base+"/p/b1"))
		case "/a-notes.xml":
			fmt.Fprint(w, sitemapOf(base+"/n/a2", base+"/n/a1"))
		default:
			syntheticPageServer(w, r)
		}
	})
	storeDir := filepath.Join(t.TempDir(), "store")
	recipePath := selectionRecipe(t, site)
	line := summaryOfDiscover(t, recipePath, storeDir, "--limit", "3")
	requireSummary(t, line, "4 found, 3 new, 0 off-site, 1 beyond the limit")
	requireLedgerPaths(t, site.URL, storeDir, []string{"/p/b2", "/p/b1", "/n/a2"})
}

// Settled rows that sit apart from the front of the list: counting only some states would
// leave room under the limit that the first unrecorded address then takes.
func TestDiscoverLimitCountsTheRowsOfEveryState(t *testing.T) {
	site := newSelectionSite(t, selectionSitePaths...)
	storeDir := filepath.Join(t.TempDir(), "store")
	recipePath := selectionRecipe(t, site)
	summaryOfDiscover(t, recipePath, storeDir, "--paths", "/essays/", "--limit", "3")
	list := writeListFile(t, site.URL+"/essays/on-time", site.URL+"/essays/on-salt")
	// One of the two pages is gone, which makes this fetch end short; it is the failed row that is wanted.
	runHoardBinary(t, "fetch", "--store", storeDir, "--list", list, "--rate", "1000")
	states := ledgerStates(t, storeDir)
	for path, want := range map[string]string{
		"/essays/on-time": store.StateOK,
		"/essays/on-salt": store.StateFailed,
		"/essays/on-rain": store.StatePending,
	} {
		if states[site.URL+path] != want {
			t.Fatalf("%s is %q, want %q", path, states[site.URL+path], want)
		}
	}

	// Three chosen addresses are held, whatever became of them, so the limit is met.
	line := summaryOfDiscover(t, recipePath, storeDir, "--limit", "3")
	requireSummary(t, line, "10 found, 0 new, 2 off-site, 7 beyond the limit")
	if after := ledgerStates(t, storeDir); fmt.Sprint(states) != fmt.Sprint(after) {
		t.Errorf("the ledger changed:\n before %v\n after  %v", states, after)
	}
}

func TestDiscoverLimitBeyondTheChosenAddressesRecordsThemAll(t *testing.T) {
	site := newSelectionSite(t, selectionSitePaths...)
	recipePath := selectionRecipe(t, site)

	everything := filepath.Join(t.TempDir(), "store")
	requireSummary(t, summaryOfDiscover(t, recipePath, everything, "--limit", "50"), "10 found, 10 new, 2 off-site")
	requireLedgerPaths(t, site.URL, everything, selectionSitePaths)

	// Six is more than the four essays and fewer than the ten pages: what counts is the four.
	essays := filepath.Join(t.TempDir(), "store")
	requireSummary(t, summaryOfDiscover(t, recipePath, essays, "--paths", "/essays/", "--limit", "6"), "4 found, 4 new, 2 off-site")
	requireLedgerPaths(t, site.URL, essays, []string{"/essays/on-time", "/essays/on-rain", "/essays/on-salt", "/essays/on-fire"})
}

func TestDiscoverRandomPicksAmongTheAddressesNotYetRecorded(t *testing.T) {
	site := newSelectionSite(t, selectionSitePaths...)
	storeDir := filepath.Join(t.TempDir(), "store")
	recipePath := selectionRecipe(t, site)

	line := summaryOfDiscover(t, recipePath, storeDir, "--random", "--limit", "3")
	requireSummary(t, line, "10 found, 3 new, 2 off-site, 7 beyond the limit")
	picked := ledgerPathsOf(t, site.URL, storeDir)
	if len(picked) != 3 {
		t.Fatalf("the ledger holds %d rows, want 3: %v", len(picked), picked)
	}
	for _, path := range picked {
		if !slices.Contains(selectionSitePaths, path) {
			t.Errorf("%s was picked and is not one of the site's pages", path)
		}
	}

	// A pick that ignored what the ledger holds would, by chance, choose the same three about
	// once in a hundred and twenty calls, so the call is repeated.
	for range 3 {
		line = summaryOfDiscover(t, recipePath, storeDir, "--random", "--limit", "3")
		requireSummary(t, line, "10 found, 0 new, 2 off-site, 7 beyond the limit")
		requireLedgerPaths(t, site.URL, storeDir, picked)
	}

	line = summaryOfDiscover(t, recipePath, storeDir, "--random", "--limit", "5")
	requireSummary(t, line, "10 found, 2 new, 2 off-site, 5 beyond the limit")
	topped := ledgerPathsOf(t, site.URL, storeDir)
	if len(topped) != 5 {
		t.Fatalf("the ledger holds %d rows after raising the limit to 5: %v", len(topped), topped)
	}
	for _, path := range picked {
		if !slices.Contains(topped, path) {
			t.Errorf("%s was picked first and is gone after raising the limit", path)
		}
	}
}

// Each pick is one of 120, so eight that all agree are not a random choice.
func TestDiscoverRandomDoesNotJustTakeTheFirstAddresses(t *testing.T) {
	site := newSelectionSite(t, selectionSitePaths...)
	recipePath := selectionRecipe(t, site)
	picks := map[string]bool{}
	for range 8 {
		storeDir := filepath.Join(t.TempDir(), "store")
		summaryOfDiscover(t, recipePath, storeDir, "--random", "--limit", "3")
		picks[strings.Join(ledgerPathsOf(t, site.URL, storeDir), " ")] = true
	}
	if len(picks) < 2 {
		t.Errorf("eight random picks of 3 out of 10 were the same every time: %v", picks)
	}
}

func TestDiscoverLimitCountsOnlyTheChosenAddresses(t *testing.T) {
	site := newSelectionSite(t, selectionSitePaths...)
	storeDir := filepath.Join(t.TempDir(), "store")
	recipePath := selectionRecipe(t, site)
	summaryOfDiscover(t, recipePath, storeDir, "--paths", "/p/", "--limit", "2")
	summaryOfDiscover(t, recipePath, storeDir, "--paths", "/essays/", "--limit", "3")
	requireLedgerPaths(t, site.URL, storeDir, []string{
		"/p/walnut", "/p/apple",
		"/essays/on-time", "/essays/on-rain", "/essays/on-salt",
	})

	line := summaryOfDiscover(t, recipePath, storeDir, "--paths", "/essays/", "--limit", "4")
	requireSummary(t, line, "4 found, 1 new, 2 off-site")
	requireLedgerPaths(t, site.URL, storeDir, []string{
		"/p/walnut", "/p/apple",
		"/essays/on-time", "/essays/on-rain", "/essays/on-salt", "/essays/on-fire",
	})
}

func TestDiscoverExitsTwoWhenTheSelectionFlagsCannotBeRead(t *testing.T) {
	site := newSelectionSite(t, selectionSitePaths...)
	recipePath := selectionRecipe(t, site)
	storeDir := filepath.Join(t.TempDir(), "store")
	cases := []struct {
		name  string
		flags []string
		says  string
	}{
		{"random without a limit", []string{"--random"}, "--random needs --limit"},
		{"random with a path but no limit", []string{"--random", "--paths", "/p/"}, "--random needs --limit"},
		{"a limit of zero", []string{"--limit", "0"}, "must be a whole number, at least 1"},
		{"a negative limit", []string{"--limit", "-2"}, "must be a whole number, at least 1"},
		{"a limit that is not a number", []string{"--limit", "many"}, "must be a whole number, at least 1"},
		{"an empty paths", []string{"--paths", ""}, "empty prefix"},
		{"a trailing comma", []string{"--paths", "/p/,"}, "empty prefix"},
		{"a leading comma", []string{"--paths", ",/p/"}, "empty prefix"},
		{"a prefix with no slash", []string{"--paths", "p/"}, `"p/" does not start with /`},
		{"a second prefix with no slash", []string{"--paths", "/p/,essays/"}, `"essays/" does not start with /`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := discoverWithSelection(t, recipePath, storeDir, c.flags...)
			if run.exitCode != 2 {
				t.Errorf("exit code %d, want 2; stdout: %s", run.exitCode, run.stdout)
			}
			if !strings.Contains(run.stderr, c.says) {
				t.Errorf("stderr does not say %q:\n%s", c.says, run.stderr)
			}
		})
	}
	if site.hitsOn("/sitemap.xml") != 0 {
		t.Errorf("the site was asked for its sitemap %d times by commands that could not be read", site.hitsOn("/sitemap.xml"))
	}
	if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
		t.Errorf("a command line that could not be read left a store behind (stat error: %v)", err)
	}
}

func TestDiscoverSaysHowManyChosenAddressesTheLimitLeftOut(t *testing.T) {
	site := newSelectionSite(t, selectionSitePaths...)
	storeDir := filepath.Join(t.TempDir(), "store")
	recipePath := selectionRecipe(t, site)
	// Found counts what the paths chose, four essays out of ten pages, not the pages.
	requireSummary(t, summaryOfDiscover(t, recipePath, storeDir, "--paths", "/essays/", "--limit", "3"),
		"4 found, 3 new, 2 off-site, 1 beyond the limit")
	requireSummary(t, summaryOfDiscover(t, recipePath, storeDir, "--paths", "/essays/", "--limit", "4"),
		"4 found, 1 new, 2 off-site")
	requireSummary(t, summaryOfDiscover(t, recipePath, storeDir, "--paths", "/essays/"),
		"4 found, 0 new, 2 off-site")
}

func TestDiscoverPathsThatChooseNothingFailAndLeaveTheLedgerAsItWas(t *testing.T) {
	site := newSelectionSite(t, selectionSitePaths...)
	recipePath := selectionRecipe(t, site)

	t.Run("no store is created", func(t *testing.T) {
		storeDir := filepath.Join(t.TempDir(), "store")
		run := discoverWithSelection(t, recipePath, storeDir, "--paths", "/absent/,/nowhere/")
		if run.exitCode != 1 {
			t.Errorf("exit code %d, want 1; stderr: %s", run.exitCode, run.stderr)
		}
		if !strings.Contains(run.stderr, "/absent/,/nowhere/ chooses none of the 10 addresses found") {
			t.Errorf("stderr does not say what the paths chose:\n%s", run.stderr)
		}
		if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
			t.Errorf("a discovery that chose nothing left a store behind (stat error: %v)", err)
		}
	})

	t.Run("an existing ledger keeps its rows", func(t *testing.T) {
		storeDir := filepath.Join(t.TempDir(), "store")
		summaryOfDiscover(t, recipePath, storeDir, "--limit", "4")
		before := ledgerStates(t, storeDir)
		if run := discoverWithSelection(t, recipePath, storeDir, "--paths", "/absent/"); run.exitCode != 1 {
			t.Errorf("exit code %d, want 1", run.exitCode)
		}
		if after := ledgerStates(t, storeDir); fmt.Sprint(before) != fmt.Sprint(after) {
			t.Errorf("a discovery that chose nothing changed the ledger:\n before %v\n after  %v", before, after)
		}
	})
}

// The flags choose among a complete list. A limit already met by the first sitemap must not
// stop the second from being read and failing the discovery.
func TestDiscoverWithSelectionFlagsStillFailsOnAnIncompleteList(t *testing.T) {
	site := newCLISite(t, func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/sitemap.xml":
			fmt.Fprint(w, selectionSitemapIndexOf(base+"/first.xml", base+"/second.xml"))
		case "/first.xml":
			fmt.Fprint(w, sitemapOf(base+"/p/one", base+"/p/two", base+"/p/three"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	recipePath := selectionRecipe(t, site)
	storeDir := filepath.Join(t.TempDir(), "store")
	run := discoverWithSelection(t, recipePath, storeDir, "--paths", "/p/", "--limit", "1")
	if run.exitCode != 1 {
		t.Errorf("exit code %d, want 1; stdout: %s", run.exitCode, run.stdout)
	}
	if !strings.Contains(run.stderr, "500") {
		t.Errorf("stderr does not name the answer that failed it: %s", run.stderr)
	}
	if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
		t.Errorf("a failed discovery left a store behind (stat error: %v)", err)
	}
}

func TestDiscoverHelpNamesTheSelectionFlags(t *testing.T) {
	run := runHoardBinary(t, "discover", "-h")
	if run.exitCode != 0 {
		t.Fatalf("exit code %d, want 0", run.exitCode)
	}
	for _, flag := range []string{"--paths", "--limit", "--random"} {
		if !strings.Contains(run.stderr, flag) {
			t.Errorf("the usage does not name %s:\n%s", flag, run.stderr)
		}
	}
}
