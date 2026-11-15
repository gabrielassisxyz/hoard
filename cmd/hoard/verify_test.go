package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabrielassisxyz/hoard/internal/store"
)

// withVerifyTable copies a recipe and adds a [verify] table to the copy.
func withVerifyTable(t *testing.T, recipePath string, lines ...string) string {
	t.Helper()
	original, err := os.ReadFile(recipePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "recipe.toml")
	content := string(original) + "\n[verify]\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// capturedSite discovers and fetches a site of three pages, one of which is gone, and
// returns the site, its plain recipe and the store.
func capturedSite(t *testing.T) (*recipeSite, string, string) {
	t.Helper()
	site := newRecipeSite(t, 0, "/a", "/b", "/gone/c")
	recipePath := site.recipe(t)
	storeDir := site.discovered(t, recipePath)
	if run := runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", recipePath); run.exitCode != 1 {
		t.Fatalf("fetch: exit code %d, want 1 for a site with a dead link; stderr: %s", run.exitCode, run.stderr)
	}
	return site, recipePath, storeDir
}

func TestVerifyExitsZeroWhenTheCriterionIsMet(t *testing.T) {
	site, recipePath, storeDir := capturedSite(t)
	criterion := withVerifyTable(t, recipePath, "min_pages = 2", "min_body_bytes = 5", `forbidden = ["Subscribe to read"]`)
	hitsBefore := site.hitsOn("/a")
	run := runHoardBinary(t, "verify", "--recipe", criterion, "--store", storeDir)
	if run.exitCode != 0 {
		t.Fatalf("exit code %d, stdout: %s stderr: %s", run.exitCode, run.stdout, run.stderr)
	}
	// The dead link is reported and does not fail the criterion.
	if got := strings.TrimSpace(run.stdout); got != "verify: ok, 2 pages held (1 failed, 0 pending)" {
		t.Errorf("stdout = %q", got)
	}
	if site.hitsOn("/a") != hitsBefore {
		t.Error("verify asked the site for a page; it reads the store only")
	}
}

func TestVerifyExitsOneAndNamesWhatBreaksTheCriterion(t *testing.T) {
	site, recipePath, storeDir := capturedSite(t)
	cases := []struct {
		name     string
		lines    []string
		mentions []string
	}{
		{"too few pages", []string{"min_pages = 3"}, []string{"pages held: 2, at least 3 required"}},
		// The synthetic page for /a is "<p>/a</p>", nine bytes.
		{"a body too small", []string{"min_body_bytes = 10"}, []string{"smaller than 10 bytes: 2 pages", site.URL + "/a", site.URL + "/b"}},
		{"a forbidden text", []string{`forbidden = ["<p>/b</p>"]`}, []string{`containing "<p>/b</p>": 1 pages`, site.URL + "/b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := runHoardBinary(t, "verify", "--recipe", withVerifyTable(t, recipePath, c.lines...), "--store", storeDir)
			if run.exitCode != 1 {
				t.Errorf("exit code %d, want 1; stdout: %s", run.exitCode, run.stdout)
			}
			for _, mention := range append(c.mentions, "verify: not met") {
				if !strings.Contains(run.stdout, mention) {
					t.Errorf("stdout does not say %q:\n%s", mention, run.stdout)
				}
			}
		})
	}
}

func TestVerifyAndStatusRefuseADirectoryThatHoldsNoStore(t *testing.T) {
	site := newRecipeSite(t, 0, "/a")
	recipePath := site.recipe(t)
	absent := filepath.Join(t.TempDir(), "mistyped")
	emptyDir := t.TempDir()
	for _, c := range []struct {
		name string
		args []string
	}{
		{"verify, no directory", []string{"verify", "--recipe", recipePath, "--store", absent}},
		{"status, no directory", []string{"status", "--store", absent}},
		{"verify, an empty directory", []string{"verify", "--recipe", recipePath, "--store", emptyDir}},
		{"status, an empty directory", []string{"status", "--store", emptyDir}},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := runHoardBinary(t, c.args...)
			if run.exitCode != 1 {
				t.Errorf("exit code %d, want 1; stderr: %s", run.exitCode, run.stderr)
			}
			if !strings.Contains(run.stderr, "no store found") {
				t.Errorf("stderr does not say there is no store: %s", run.stderr)
			}
		})
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Errorf("a read-only command created the directory it was asked to read (stat error: %v)", err)
	}
	if entries, err := os.ReadDir(emptyDir); err != nil || len(entries) != 0 {
		t.Errorf("a read-only command left %d entries in an empty directory (err %v)", len(entries), err)
	}
}

func TestVerifyAndStatusExitTwoWhenTheCommandLineCannotBeRead(t *testing.T) {
	_, recipePath, storeDir := capturedSite(t)
	invalid := writeRecipeFile(t, "http://127.0.0.1:1", "kind = \"crawl\"")
	cases := []struct {
		name    string
		args    []string
		mention string
	}{
		{"verify without a recipe", []string{"verify", "--store", storeDir}, "--recipe is required"},
		{"verify without a store", []string{"verify", "--recipe", recipePath}, "--store is required"},
		{"verify with a stray argument", []string{"verify", "--recipe", recipePath, "--store", storeDir, "extra"}, "unexpected arguments"},
		{"verify with a recipe that does not exist", []string{"verify", "--recipe", filepath.Join(t.TempDir(), "absent.toml"), "--store", storeDir}, "absent.toml"},
		{"verify with an invalid recipe", []string{"verify", "--recipe", invalid, "--store", storeDir}, "discover.kind"},
		{"status without a store", []string{"status"}, "--store is required"},
		{"status with a stray argument", []string{"status", "--store", storeDir, "extra"}, "unexpected arguments"},
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
}

func TestStatusSaysWhereACaptureStands(t *testing.T) {
	site := newRecipeSite(t, 0, "/a", "/b", "/gone/c")
	recipePath := site.recipe(t)
	storeDir := site.discovered(t, recipePath)

	before := runHoardBinary(t, "status", "--store", storeDir)
	if before.exitCode != 0 || strings.TrimSpace(before.stdout) != "0 held, 0 failed, 3 pending" {
		t.Errorf("before the fetch: exit code %d, stdout %q", before.exitCode, before.stdout)
	}

	runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", recipePath)
	hitsBefore := site.hitsOn("/a")
	after := runHoardBinary(t, "status", "--store", storeDir)
	// A capture with a failed address is still a status that could be reported.
	if after.exitCode != 0 {
		t.Errorf("exit code %d, want 0", after.exitCode)
	}
	lines := strings.Split(strings.TrimSpace(after.stdout), "\n")
	if lines[0] != "2 held, 1 failed, 0 pending" {
		t.Errorf("first line = %q", lines[0])
	}
	wantFailed := "failed: " + site.URL + "/gone/c: HTTP 404 Not Found"
	if len(lines) != 2 || lines[1] != wantFailed {
		t.Errorf("failed rows = %q, want exactly %q", lines[1:], wantFailed)
	}
	if site.hitsOn("/a") != hitsBefore {
		t.Error("status asked the site for a page; it reads the store only")
	}
}

func TestFetchByRecipeOnAStoreWithAnEmptyLedgerSaysSo(t *testing.T) {
	site := newRecipeSite(t, 0, "/a")
	storeDir := filepath.Join(t.TempDir(), "store")
	st, err := store.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	run := runHoardBinary(t, "fetch", "--store", storeDir, "--recipe", site.recipe(t))
	if run.exitCode != 1 {
		t.Errorf("exit code %d, want 1 for a ledger with nothing to fetch", run.exitCode)
	}
	if !strings.Contains(run.stderr, "hoard discover") {
		t.Errorf("stderr does not name the command that fills the ledger: %s", run.stderr)
	}
}

func TestTopLevelUsageNamesVerifyAndStatus(t *testing.T) {
	run := runHoardBinary(t, "help")
	for _, command := range []string{"verify", "status"} {
		if !strings.Contains(run.stdout, command) {
			t.Errorf("the usage does not name %s:\n%s", command, run.stdout)
		}
	}
}
