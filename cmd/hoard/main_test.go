package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
)

// These tests run the compiled program as a separate process, the way a person or a script
// does, so that what they check is the exit code the operating system sees and what lands
// on the real standard streams.

var (
	hoardBinaryOnce sync.Once
	hoardBinaryDir  string
	hoardBinaryPath string
	hoardBinaryErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if hoardBinaryDir != "" {
		os.RemoveAll(hoardBinaryDir)
	}
	os.Exit(code)
}

// builtHoardBinary compiles the command once, the way AGENTS.md says to build it: from the
// repository root, with cgo off.
func builtHoardBinary(t *testing.T) string {
	t.Helper()
	hoardBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "hoard-binary-")
		if err != nil {
			hoardBinaryErr = err
			return
		}
		hoardBinaryDir = dir
		path := filepath.Join(dir, "hoard")
		build := exec.Command("go", "build", "-o", path, "./cmd/hoard")
		build.Dir = filepath.Join("..", "..")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := build.CombinedOutput(); err != nil {
			hoardBinaryErr = fmt.Errorf("CGO_ENABLED=0 go build failed: %v\n%s", err, output)
			return
		}
		hoardBinaryPath = path
	})
	if hoardBinaryErr != nil {
		t.Fatal(hoardBinaryErr)
	}
	return hoardBinaryPath
}

type hoardRun struct {
	stdout, stderr string
	exitCode       int
}

func runHoardBinary(t *testing.T, args ...string) hoardRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, builtHoardBinary(t), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := hoardRun{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		result.exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("could not run hoard: %v", err)
	}
	return result
}

// cliSite is a synthetic web server that keeps what the command line tests need to see.
type cliSite struct {
	*httptest.Server
	mu       sync.Mutex
	hits     map[string]int
	agents   []string
	arrivals []time.Time
	inFlight int
	peak     int
}

func newCLISite(t *testing.T, serve http.HandlerFunc) *cliSite {
	t.Helper()
	site := &cliSite{hits: map[string]int{}}
	site.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		site.mu.Lock()
		site.hits[r.URL.Path]++
		site.agents = append(site.agents, r.UserAgent())
		site.arrivals = append(site.arrivals, time.Now())
		site.inFlight++
		site.peak = max(site.peak, site.inFlight)
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

func (s *cliSite) hitsOn(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func (s *cliSite) peakInFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

// arrivalSpan is the time from the first request to the last.
func (s *cliSite) arrivalSpan() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.arrivals[len(s.arrivals)-1].Sub(s.arrivals[0])
}

func writeListFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "list.txt")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func syntheticPageServer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<p>%s</p>", r.URL.Path)
}

func TestBinaryIsBuiltWithoutCGO(t *testing.T) {
	binary := builtHoardBinary(t)
	output, err := exec.Command("go", "version", "-m", binary).CombinedOutput()
	if err != nil {
		t.Fatalf("go version -m: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "CGO_ENABLED=0") {
		t.Fatalf("the binary was not built with CGO_ENABLED=0:\n%s", output)
	}
}

func TestBinaryExitsZeroWhenEveryAddressIsHeld(t *testing.T) {
	site := newCLISite(t, syntheticPageServer)
	store := filepath.Join(t.TempDir(), "store")
	list := writeListFile(t, "# three pages", site.URL+"/a", "", site.URL+"/b", "  "+site.URL+"/c  ")

	first := runHoardBinary(t, "fetch", "--store", store, "--list", list, "--rate", "1000")
	if first.exitCode != 0 || first.stderr != "" {
		t.Fatalf("first run: exit %d, stderr %q", first.exitCode, first.stderr)
	}
	if want := "3 listed, 3 held, 0 failed, 0 pending\n"; first.stdout != want {
		t.Fatalf("first run printed %q, want %q", first.stdout, want)
	}

	second := runHoardBinary(t, "fetch", "--store", store, "--list", list, "--rate", "1000")
	if second.exitCode != 0 || second.stdout != first.stdout {
		t.Fatalf("second run: exit %d, stdout %q", second.exitCode, second.stdout)
	}
	for _, path := range []string{"/a", "/b", "/c"} {
		if got := site.hitsOn(path); got != 1 {
			t.Errorf("%s was requested %d times over two runs, want once", path, got)
		}
	}
}

func TestBinaryExitsOneWhenAnAddressCouldNotBeFetched(t *testing.T) {
	site := newCLISite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		syntheticPageServer(w, r)
	})
	store := filepath.Join(t.TempDir(), "store")
	list := writeListFile(t, site.URL+"/present", site.URL+"/missing")

	for run := 1; run <= 2; run++ {
		result := runHoardBinary(t, "fetch", "--store", store, "--list", list, "--rate", "1000")
		if result.exitCode != 1 {
			t.Fatalf("run %d: exit %d, want 1 (stderr %q)", run, result.exitCode, result.stderr)
		}
		if want := "2 listed, 1 held, 1 failed, 0 pending\n"; result.stdout != want {
			t.Fatalf("run %d printed %q, want %q", run, result.stdout, want)
		}
	}
	if got := site.hitsOn("/missing"); got != 1 {
		t.Errorf("the missing page was requested %d times over two runs, want once", got)
	}
}

func TestBinaryExitsOneWhenTheListCannotBeRead(t *testing.T) {
	store := filepath.Join(t.TempDir(), "store")
	missing := filepath.Join(t.TempDir(), "no-such-list.txt")

	result := runHoardBinary(t, "fetch", "--store", store, "--list", missing)

	if result.exitCode != 1 || !strings.Contains(result.stderr, "no-such-list.txt") || result.stdout != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", result.exitCode, result.stdout, result.stderr)
	}
}

func TestBinaryExitsTwoWithUsageWhenTheCommandLineCannotBeRead(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	list := writeListFile(t, "http://127.0.0.1:1/never-requested")
	valid := []string{"fetch", "--store", storeDir, "--list", list}
	with := func(extra ...string) []string { return append(append([]string{}, valid...), extra...) }

	cases := map[string]struct {
		args    []string
		mention string
	}{
		"no command":             {nil, "usage: hoard"},
		"unknown command":        {[]string{"bogus"}, `unknown command "bogus"`},
		"no flags":               {[]string{"fetch"}, "--store is required"},
		"no list":                {[]string{"fetch", "--store", storeDir}, "--list is required"},
		"no store":               {[]string{"fetch", "--list", list}, "--store is required"},
		"unknown flag":           {with("--nope"), "flag provided but not defined"},
		"stray argument":         {with("extra"), "unexpected arguments"},
		"zero workers":           {with("--workers", "0"), "--workers must be at least 1"},
		"workers not a number":   {with("--workers", "many"), "usage: hoard fetch"},
		"zero rate":              {with("--rate", "0"), "--rate must be a positive"},
		"negative rate":          {with("--rate", "-3"), "--rate must be a positive"},
		"rate not a number":      {with("--rate", "NaN"), "--rate must be a positive"},
		"zero timeout":           {with("--timeout", "0s"), "--timeout must be longer than zero"},
		"timeout not a duration": {with("--timeout", "soon"), "usage: hoard fetch"},
		"empty user agent":       {with("--user-agent", ""), "--user-agent cannot be empty"},
		"flag without its value": {with("--rate"), "usage: hoard fetch"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			result := runHoardBinary(t, c.args...)
			if result.exitCode != 2 {
				t.Fatalf("exit %d, want 2 (stderr %q)", result.exitCode, result.stderr)
			}
			if !strings.Contains(result.stderr, "usage: hoard") || !strings.Contains(result.stderr, c.mention) {
				t.Errorf("stderr lacks a usage message mentioning %q:\n%s", c.mention, result.stderr)
			}
			if result.stdout != "" {
				t.Errorf("stdout = %q, want nothing", result.stdout)
			}
		})
	}
	if _, err := os.Stat(storeDir); err == nil {
		t.Errorf("a command line that could not be read created the store at %s", storeDir)
	}
}

func TestBinaryHelpIsNotAnError(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"fetch", "-h"}} {
		result := runHoardBinary(t, args...)
		if result.exitCode != 0 || !strings.Contains(result.stdout+result.stderr, "usage: hoard") {
			t.Errorf("hoard %v: exit %d, stdout %q, stderr %q", args, result.exitCode, result.stdout, result.stderr)
		}
	}
}

func TestBinaryFetchHelpShowsTheDocumentedDefaults(t *testing.T) {
	result := runHoardBinary(t, "fetch", "-h")
	if result.exitCode != 0 {
		t.Fatalf("exit %d", result.exitCode)
	}
	for _, want := range []string{
		"-workers int", "(default 4)",
		"-rate float", "(default 2)",
		"-timeout duration", "(default 30s)",
		"-user-agent string", fetch.DefaultUserAgent,
		"-store string", "-list string",
	} {
		if !strings.Contains(result.stdout+result.stderr, want) {
			t.Errorf("fetch -h does not mention %q:\n%s%s", want, result.stdout, result.stderr)
		}
	}
}

func TestBinaryPassesItsFlagsToTheFetch(t *testing.T) {
	t.Run("user agent", func(t *testing.T) {
		site := newCLISite(t, syntheticPageServer)
		list := writeListFile(t, site.URL+"/a")
		for _, c := range []struct {
			flags []string
			want  string
		}{
			{nil, fetch.DefaultUserAgent},
			{[]string{"--user-agent", "another-agent/2"}, "another-agent/2"},
		} {
			store := filepath.Join(t.TempDir(), "store")
			args := append([]string{"fetch", "--store", store, "--list", list, "--rate", "1000"}, c.flags...)
			if result := runHoardBinary(t, args...); result.exitCode != 0 {
				t.Fatalf("exit %d: %s", result.exitCode, result.stderr)
			}
			site.mu.Lock()
			got := site.agents[len(site.agents)-1]
			site.mu.Unlock()
			if got != c.want {
				t.Errorf("User-Agent = %q, want %q", got, c.want)
			}
		}
	})

	t.Run("workers", func(t *testing.T) {
		site := newCLISite(t, func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
			syntheticPageServer(w, r)
		})
		var lines []string
		for i := range 8 {
			lines = append(lines, fmt.Sprintf("%s/w%d", site.URL, i))
		}
		store := filepath.Join(t.TempDir(), "store")
		result := runHoardBinary(t, "fetch", "--store", store, "--list", writeListFile(t, lines...), "--rate", "1000", "--workers", "2")
		if result.exitCode != 0 {
			t.Fatalf("exit %d: %s", result.exitCode, result.stderr)
		}
		if peak := site.peakInFlight(); peak != 2 {
			t.Errorf("the server saw %d requests at once with --workers 2", peak)
		}
	})

	t.Run("rate", func(t *testing.T) {
		site := newCLISite(t, syntheticPageServer)
		var lines []string
		for i := range 6 {
			lines = append(lines, fmt.Sprintf("%s/r%d", site.URL, i))
		}
		store := filepath.Join(t.TempDir(), "store")
		result := runHoardBinary(t, "fetch", "--store", store, "--list", writeListFile(t, lines...), "--rate", "20")
		if result.exitCode != 0 {
			t.Fatalf("exit %d: %s", result.exitCode, result.stderr)
		}
		span := site.arrivalSpan()
		// Six requests at 20 a second are five intervals of 50ms; the default of 2 a second
		// would take 2.5s.
		if span < 200*time.Millisecond || span > 1500*time.Millisecond {
			t.Errorf("six requests with --rate 20 arrived over %v, want about 250ms", span)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		site := newCLISite(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
				syntheticPageServer(w, r)
			}
		})
		store := filepath.Join(t.TempDir(), "store")
		started := time.Now()
		result := runHoardBinary(t, "fetch", "--store", store, "--list", writeListFile(t, site.URL+"/slow"), "--rate", "1000", "--timeout", "100ms")
		if result.exitCode != 1 || result.stdout != "1 listed, 0 held, 1 failed, 0 pending\n" {
			t.Fatalf("exit %d, stdout %q, stderr %q", result.exitCode, result.stdout, result.stderr)
		}
		if elapsed := time.Since(started); elapsed > 2500*time.Millisecond {
			t.Errorf("took %v with --timeout 100ms", elapsed)
		}
	})
}

func TestAgentsBriefingNamesTheRealCommandsAndWhatExists(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	briefing := string(raw)
	for _, want := range []string{
		"CGO_ENABLED=0 go build -o hoard ./cmd/hoard",
		"go test ./...",
		"`hoard fetch` exists",
	} {
		if !strings.Contains(briefing, want) {
			t.Errorf("AGENTS.md does not say %q", want)
		}
	}
	for _, stale := range []string{"nothing is implemented", "none yet. They arrive"} {
		if strings.Contains(briefing, stale) {
			t.Errorf("AGENTS.md still says %q", stale)
		}
	}
}
