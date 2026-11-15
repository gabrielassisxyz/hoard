package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

var (
	cliPNG = []byte("\x89PNG synthetic cli one \x00\x01")
	cliGIF = []byte("GIF89a synthetic cli two \x00\x02")
)

// imageHost serves two synthetic images and an error page with status 200, and remembers
// whether any request it got carried a Cookie header.
type imageHost struct {
	*httptest.Server
	mu         sync.Mutex
	requests   int
	cookieSeen bool
}

func newImageHost(t *testing.T) *imageHost {
	t.Helper()
	host := &imageHost{}
	host.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host.mu.Lock()
		host.requests++
		host.cookieSeen = host.cookieSeen || len(r.Header.Values("Cookie")) > 0
		host.mu.Unlock()
		switch r.URL.Path {
		case "/b.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(cliPNG)
		case "/a.gif":
			w.Header().Set("Content-Type", "image/gif")
			w.Write(cliGIF)
		default:
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<p>an error page</p>"))
		}
	}))
	t.Cleanup(host.Close)
	return host
}

func (h *imageHost) requestCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requests
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestAssetsListAndStatusKeepImagesApartFromPages(t *testing.T) {
	_, recipePath, storeDir := capturedSite(t)
	criterion := withVerifyTable(t, recipePath, "min_pages = 2")
	pageLines := runHoardBinary(t, "list", "--store", storeDir, "--json")
	verifyBefore := runHoardBinary(t, "verify", "--recipe", criterion, "--store", storeDir)
	statusBefore := runHoardBinary(t, "status", "--store", storeDir)

	first, second := newImageHost(t), newImageHost(t)
	list := writeListFile(t, second.URL+"/b.png", first.URL+"/a.gif")
	run := runHoardBinary(t, "assets", "--store", storeDir, "--list", list, "--rate", "1000")
	if run.exitCode != 0 || strings.TrimSpace(run.stdout) != "2 listed, 2 held, 0 failed, 0 pending" {
		t.Fatalf("assets: exit %d, stdout %q, stderr %q", run.exitCode, run.stdout, run.stderr)
	}
	if first.cookieSeen || second.cookieSeen {
		t.Error("a request of the assets command carried a Cookie header")
	}

	assets := runHoardBinary(t, "list", "--store", storeDir, "--assets", "--json")
	lines := strings.Split(strings.TrimSuffix(assets.stdout, "\n"), "\n")
	if assets.exitCode != 0 || len(lines) != 2 {
		t.Fatalf("list --assets: exit %d, lines %q, stderr %q", assets.exitCode, lines, assets.stderr)
	}
	wantKeys := []string{"address", "final_address", "content_type", "sha256", "size_bytes", "file_path"}
	// Ports are random, so which host sorts first is not known until the run.
	type kept struct {
		address   string
		body      []byte
		extension string
	}
	expected := []kept{{first.URL + "/a.gif", cliGIF, ".gif"}, {second.URL + "/b.png", cliPNG, ".png"}}
	slices.SortFunc(expected, func(x, y kept) int { return strings.Compare(x.address, y.address) })
	absStore, _ := filepath.Abs(storeDir)
	for i, line := range lines {
		if got := listTestKeyOrder(t, line); !slices.Equal(got, wantKeys) {
			t.Errorf("keys of %s are %v, want %v", line, got, wantKeys)
		}
		var entry listedAsset
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		want := expected[i]
		hash := sha256Hex(want.body)
		wantPath := filepath.Join(absStore, "assets", hash[:2], hash+want.extension)
		if entry.Address != want.address || entry.SHA256 != hash || entry.FilePath != wantPath ||
			entry.SizeBytes != int64(len(want.body)) || entry.FinalAddress != entry.Address {
			t.Errorf("line %d = %+v, want address %s, sha %s, path %s", i, entry, want.address, hash, wantPath)
		}
		if got, err := os.ReadFile(entry.FilePath); err != nil || !bytes.Equal(got, want.body) {
			t.Errorf("file_path %s does not hold the image: %v", entry.FilePath, err)
		}
	}

	if after := runHoardBinary(t, "list", "--store", storeDir, "--json"); after.stdout != pageLines.stdout {
		t.Errorf("list --json changed after assets:\n%s\nwas\n%s", after.stdout, pageLines.stdout)
	}
	verifyAfter := runHoardBinary(t, "verify", "--recipe", criterion, "--store", storeDir)
	if verifyAfter.exitCode != verifyBefore.exitCode || verifyAfter.stdout != verifyBefore.stdout {
		t.Errorf("verify changed after assets: %d %q, was %d %q", verifyAfter.exitCode, verifyAfter.stdout, verifyBefore.exitCode, verifyBefore.stdout)
	}
	statusAfter := strings.Split(strings.TrimSpace(runHoardBinary(t, "status", "--store", storeDir).stdout), "\n")
	statusPageLine := strings.Split(strings.TrimSpace(statusBefore.stdout), "\n")[0]
	if statusAfter[0] != statusPageLine || statusAfter[1] != "assets: 2 held, 0 failed, 0 pending" {
		t.Errorf("status after = %q, want the page line %q then the assets line", statusAfter, statusPageLine)
	}
}

func TestAssetsExitCodes(t *testing.T) {
	host := newImageHost(t)
	storeDir := filepath.Join(t.TempDir(), "store")
	good := writeListFile(t, host.URL+"/a.gif")
	if run := runHoardBinary(t, "assets", "--store", storeDir, "--list", good); run.exitCode != 0 {
		t.Errorf("all held: exit %d, stderr %q", run.exitCode, run.stderr)
	}
	if _, err := os.Stat(filepath.Join(storeDir, "ledger.db")); err != nil {
		t.Errorf("assets --list did not create the store: %v", err)
	}
	mixed := writeListFile(t, host.URL+"/a.gif", host.URL+"/page")
	run := runHoardBinary(t, "assets", "--store", storeDir, "--list", mixed)
	if run.exitCode != 1 || strings.TrimSpace(run.stdout) != "2 listed, 1 held, 1 failed, 0 pending" {
		t.Errorf("one failed: exit %d, stdout %q", run.exitCode, run.stdout)
	}
	for _, args := range [][]string{{"assets", "--list", good}, {"assets", "--store", storeDir}, {"assets", "--bogus"}} {
		if run := runHoardBinary(t, args...); run.exitCode != 2 {
			t.Errorf("hoard %v: exit %d, want 2", args, run.exitCode)
		}
	}
}
