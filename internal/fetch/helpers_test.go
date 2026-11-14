package fetch_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

// Every test here runs against synthetic sites served from this process. No address, page
// or byte of HTML comes from a real site.

type siteArrival struct {
	requestURI string
	at         time.Time
	userAgent  string
}

// recordingSite is a web server that notes when each request reached it and how many were
// being served at once.
type recordingSite struct {
	*httptest.Server
	mu          sync.Mutex
	arrivals    []siteArrival
	inFlight    int
	maxInFlight int
}

func newRecordingSite(t *testing.T, serve http.HandlerFunc) *recordingSite {
	t.Helper()
	site := &recordingSite{}
	site.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		site.mu.Lock()
		site.arrivals = append(site.arrivals, siteArrival{r.URL.RequestURI(), time.Now(), r.UserAgent()})
		site.inFlight++
		site.maxInFlight = max(site.maxInFlight, site.inFlight)
		site.mu.Unlock()
		defer func() {
			site.mu.Lock()
			site.inFlight--
			site.mu.Unlock()
		}()
		serve(w, r)
	}))
	t.Cleanup(site.Close)
	return site
}

func (s *recordingSite) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.arrivals)
}

func (s *recordingSite) peakInFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInFlight
}

// arrivalTimes are the moments requests for requestURI arrived, oldest first. An empty
// requestURI means every request.
func (s *recordingSite) arrivalTimes(requestURI string) []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	var times []time.Time
	for _, a := range s.arrivals {
		if requestURI == "" || a.requestURI == requestURI {
			times = append(times, a.at)
		}
	}
	slices.SortFunc(times, time.Time.Compare)
	return times
}

func (s *recordingSite) userAgents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var agents []string
	for _, a := range s.arrivals {
		agents = append(agents, a.userAgent)
	}
	return agents
}

func (s *recordingSite) address(requestURI string) string {
	return s.URL + requestURI
}

func (s *recordingSite) addresses(requestURIs ...string) []string {
	out := make([]string, len(requestURIs))
	for i, uri := range requestURIs {
		out[i] = s.address(uri)
	}
	return out
}

// countdown answers the first n requests with failing and every later one with succeeding.
type countdown struct {
	mu   sync.Mutex
	seen int
	n    int
}

func (c *countdown) stillFailing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen++
	return c.seen <= c.n
}

func serveHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(body))
}

func dropConnection(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		conn.Close()
	}
}

func syntheticPage(label string) string {
	return "<!doctype html>\r\n<title>" + label + "</title>\r\n<p>caf\xc3\xa9 \xff  </p>\n  "
}

func openStoreAt(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func openFreshStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	return openStoreAt(t, dir), dir
}

// runFetchOver runs the fetch entry point the way the command does, with a rate high
// enough that the limiter is not what a test is measuring unless it says so.
func runFetchOver(t *testing.T, st *store.Store, addresses []string, tweaks ...func(*fetch.RunConfig)) fetch.RunSummary {
	t.Helper()
	cfg := fetch.RunConfig{Store: st, Addresses: addresses, Workers: 4, Rate: 1000, Timeout: 5 * time.Second}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	summary, err := fetch.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("fetch.Run: %v", err)
	}
	return summary
}

func rowFor(t *testing.T, st *store.Store, address string) store.Page {
	t.Helper()
	page, found, err := st.Page(address)
	if err != nil || !found {
		t.Fatalf("no ledger row for %s (found=%v, err=%v)", address, found, err)
	}
	return page
}

// storedBodyFile is where the layout says a body lives, spelled out here rather than taken
// from the store so that the test checks the layout and not the store's idea of it.
func storedBodyFile(storeDir, sha256Hex string) string {
	return filepath.Join(storeDir, "raw", sha256Hex[:2], sha256Hex+".zst")
}

// decompressedBodyFile reads a body file with an independent zstd reader.
func decompressedBodyFile(t *testing.T, path string) []byte {
	t.Helper()
	compressed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	plain, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		t.Fatalf("%s is not a zstd stream: %v", path, err)
	}
	return plain
}

func sha256HexOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func bodyFilesUnder(t *testing.T, storeDir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(storeDir, "raw"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !strings.HasSuffix(file, ".zst") {
			t.Errorf("%s is not a finished body file", file)
		}
	}
	return files
}

// mostArrivalsInAnyWindow is the largest number of arrivals that fall within one window
// of one another, counting both ends.
func mostArrivalsInAnyWindow(arrivals []time.Time, window time.Duration) int {
	best := 0
	for i := range arrivals {
		count := 0
		for j := i; j < len(arrivals) && arrivals[j].Sub(arrivals[i]) <= window; j++ {
			count++
		}
		best = max(best, count)
	}
	return best
}
