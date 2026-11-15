package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/gabrielassisxyz/hoard/internal/store"
)

// listSeed is one ledger row to put in a store before it is listed.
type listSeed struct {
	address      string
	state        string
	finalAddress string
	contentType  string
	body         string
	fetchedAt    time.Time
}

// seedListStore writes rows through the store package, the way a fetch leaves them, so the
// listing is checked against a real ledger and real body files.
func seedListStore(t *testing.T, dir string, seeds []listSeed) {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	addresses := make([]string, len(seeds))
	for i, seed := range seeds {
		addresses[i] = seed.address
	}
	if err := st.AddPending(addresses); err != nil {
		t.Fatal(err)
	}
	for _, seed := range seeds {
		switch seed.state {
		case store.StateOK:
			ref, err := st.PutBody([]byte(seed.body))
			if err != nil {
				t.Fatal(err)
			}
			err = st.MarkOK(seed.address, store.Outcome{
				FinalAddress: seed.finalAddress,
				HTTPStatus:   200,
				FetchedAt:    seed.fetchedAt,
				ContentType:  seed.contentType,
				Body:         ref,
			})
			if err != nil {
				t.Fatal(err)
			}
		case store.StateFailed:
			if err := st.MarkFailed(seed.address, store.Outcome{HTTPStatus: 404, Err: "HTTP 404"}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// runHoardBinaryIn is runHoardBinary with a working directory, for the one check that
// needs the command to resolve a relative path.
func runHoardBinaryIn(t *testing.T, workDir string, args ...string) hoardRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, builtHoardBinary(t), args...)
	cmd.Dir = workDir
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

func listedObjects(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	if stdout == "" {
		return nil
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("the listing does not end with a newline: %q", stdout)
	}
	var objects []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		var object map[string]any
		if err := json.Unmarshal([]byte(line), &object); err != nil {
			t.Fatalf("line %q is not a JSON object: %v", line, err)
		}
		objects = append(objects, object)
	}
	return objects
}

func listedAddresses(objects []map[string]any) []string {
	addresses := make([]string, len(objects))
	for i, object := range objects {
		addresses[i], _ = object["address"].(string)
	}
	return addresses
}

// keyOrderOfLine reads the keys of one JSON object in the order they were written, which a
// map cannot say.
func keyOrderOfLine(t *testing.T, line string) []string {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(line))
	if opening, err := decoder.Token(); err != nil || opening != json.Delim('{') {
		t.Fatalf("line %q does not open an object: %v", line, err)
	}
	var keys []string
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		keys = append(keys, key.(string))
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
	}
	return keys
}

func hexOfSHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// decodeBodyFile decompresses a body file with the zstd package directly, so a path that
// points at the wrong file, or at one in another format, shows up as a wrong body.
func decodeBodyFile(t *testing.T, path string) string {
	t.Helper()
	compressed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the body at %s: %v", path, err)
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	plain, err := decoder.DecodeAll(compressed, nil)
	if err != nil {
		t.Fatalf("%s is not a zstd file: %v", path, err)
	}
	return string(plain)
}

// treeOfListTest lists everything under root, directories included, with a hash of each
// file, so "created nothing" and "changed nothing" are both checked.
func treeOfListTest(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			entries = append(entries, rel+"/")
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, rel+" "+hexOfSHA256(string(content)))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return entries
}

func TestListPrintsOneLinePerHeldPageInAddressOrder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	now := time.Date(2026, 11, 14, 19, 41, 2, 0, time.UTC)
	// Neither the order they were added in nor the order of their states is the order the
	// listing has to come out in.
	seedListStore(t, dir, []listSeed{
		{address: "https://site.test/c", state: store.StateOK, body: "c", fetchedAt: now},
		{address: "https://site.test/a", state: store.StateOK, body: "a", fetchedAt: now},
		{address: "https://site.test/b", state: store.StatePending},
		{address: "https://site.test/d", state: store.StateFailed},
		{address: "https://site.test/e", state: store.StateOK, body: "e", fetchedAt: now},
	})

	result := runHoardBinary(t, "list", "--store", dir, "--json")

	if result.exitCode != 0 || result.stderr != "" {
		t.Fatalf("exit %d, stderr %q", result.exitCode, result.stderr)
	}
	got := listedAddresses(listedObjects(t, result.stdout))
	want := []string{"https://site.test/a", "https://site.test/c", "https://site.test/e"}
	if !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v: the held pages, by address, and nothing for pending or failed rows", got, want)
	}
}

func TestListLinesHaveExactlyTheDocumentedKeysInOrder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	seedListStore(t, dir, []listSeed{
		{address: "https://site.test/a", state: store.StateOK, body: "a", fetchedAt: time.Now()},
		{address: "https://site.test/b", state: store.StateOK, body: "b", fetchedAt: time.Now(), contentType: "text/html"},
	})

	result := runHoardBinary(t, "list", "--store", dir, "--json")

	want := []string{"address", "final_address", "http_status", "fetched_at", "content_type", "sha256", "size_bytes", "body_path"}
	lines := strings.Split(strings.TrimSuffix(result.stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2: %q", len(lines), result.stdout)
	}
	for _, line := range lines {
		if got := keyOrderOfLine(t, line); !slices.Equal(got, want) {
			t.Errorf("keys of %s are %v, want %v", line, got, want)
		}
	}
}

func TestListLinesCarryTheLedgersFacts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	// Not UTC on purpose: the line has to give the same instant as UTC.
	brasilia := time.FixedZone("-0300", -3*60*60)
	seedListStore(t, dir, []listSeed{
		{
			address:      "https://site.test/moved",
			state:        store.StateOK,
			finalAddress: "https://site.test/moved-here",
			contentType:  "text/html; charset=utf-8",
			body:         "<p>moved</p>",
			fetchedAt:    time.Date(2026, 11, 14, 19, 41, 2, 0, brasilia),
		},
		{
			address:   "https://site.test/plain?x=1&y=2",
			state:     store.StateOK,
			body:      "<p>no redirect, no content type</p>",
			fetchedAt: time.Date(2026, 11, 14, 19, 41, 3, 0, time.UTC),
		},
	})

	result := runHoardBinary(t, "list", "--store", dir, "--json")

	if result.exitCode != 0 {
		t.Fatalf("exit %d: %s", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stdout, `"https://site.test/plain?x=1&y=2"`) {
		t.Errorf("the address was not printed as it is, with & unescaped:\n%s", result.stdout)
	}
	want := map[string]struct {
		finalAddress, fetchedAt, contentType, body string
	}{
		"https://site.test/moved": {
			"https://site.test/moved-here", "2026-11-14T22:41:02Z", "text/html; charset=utf-8", "<p>moved</p>",
		},
		"https://site.test/plain?x=1&y=2": {
			"https://site.test/plain?x=1&y=2", "2026-11-14T19:41:03Z", "", "<p>no redirect, no content type</p>",
		},
	}
	objects := listedObjects(t, result.stdout)
	if len(objects) != len(want) {
		t.Fatalf("%d lines, want %d", len(objects), len(want))
	}
	for _, object := range objects {
		address := object["address"].(string)
		expected := want[address]
		sha := hexOfSHA256(expected.body)
		if object["final_address"] != expected.finalAddress {
			t.Errorf("%s: final_address = %v, want %q", address, object["final_address"], expected.finalAddress)
		}
		if object["http_status"] != float64(200) {
			t.Errorf("%s: http_status = %v, want 200", address, object["http_status"])
		}
		if object["fetched_at"] != expected.fetchedAt {
			t.Errorf("%s: fetched_at = %v, want %q", address, object["fetched_at"], expected.fetchedAt)
		}
		if object["content_type"] != expected.contentType {
			t.Errorf("%s: content_type = %#v, want %q", address, object["content_type"], expected.contentType)
		}
		if object["sha256"] != sha {
			t.Errorf("%s: sha256 = %v, want %s", address, object["sha256"], sha)
		}
		if object["size_bytes"] != float64(len(expected.body)) {
			t.Errorf("%s: size_bytes = %v, want %d", address, object["size_bytes"], len(expected.body))
		}
		bodyPath, _ := object["body_path"].(string)
		wantPath := filepath.Join(dir, "raw", sha[:2], sha+".zst")
		if bodyPath != wantPath {
			t.Errorf("%s: body_path = %q, want %q", address, bodyPath, wantPath)
		}
		if got := decodeBodyFile(t, bodyPath); got != expected.body {
			t.Errorf("%s: the file at body_path holds %q, want %q", address, got, expected.body)
		}
	}
}

func TestListGivesAbsoluteBodyPathsForARelativeStore(t *testing.T) {
	parent := t.TempDir()
	seedListStore(t, filepath.Join(parent, "store"), []listSeed{
		{address: "https://site.test/a", state: store.StateOK, body: "a", fetchedAt: time.Now()},
	})

	result := runHoardBinaryIn(t, parent, "list", "--store", "store", "--json")

	if result.exitCode != 0 {
		t.Fatalf("exit %d: %s", result.exitCode, result.stderr)
	}
	objects := listedObjects(t, result.stdout)
	if len(objects) != 1 {
		t.Fatalf("%d lines, want 1: %q", len(objects), result.stdout)
	}
	bodyPath, _ := objects[0]["body_path"].(string)
	if !filepath.IsAbs(bodyPath) {
		t.Fatalf("body_path %q is not absolute", bodyPath)
	}
	if got := decodeBodyFile(t, bodyPath); got != "a" {
		t.Errorf("the file at body_path holds %q, want %q", got, "a")
	}
}

func TestListExitsZeroWithNothingToPrint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	seedListStore(t, dir, []listSeed{
		{address: "https://site.test/waiting", state: store.StatePending},
		{address: "https://site.test/gone", state: store.StateFailed},
	})

	result := runHoardBinary(t, "list", "--store", dir, "--json")

	if result.exitCode != 0 || result.stdout != "" || result.stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", result.exitCode, result.stdout, result.stderr)
	}
}

func TestListLeavesTheStoreExactlyAsItFoundIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	seedListStore(t, dir, []listSeed{
		{address: "https://site.test/a", state: store.StateOK, body: "a", fetchedAt: time.Now()},
		{address: "https://site.test/b", state: store.StatePending},
	})
	before := treeOfListTest(t, dir)

	result := runHoardBinary(t, "list", "--store", dir, "--json")

	if result.exitCode != 0 {
		t.Fatalf("exit %d: %s", result.exitCode, result.stderr)
	}
	if after := treeOfListTest(t, dir); !slices.Equal(before, after) {
		t.Errorf("listing changed the store:\nbefore %v\nafter  %v", before, after)
	}
}

func TestListExitsOneAndCreatesNothingWhenThereIsNoStore(t *testing.T) {
	root := t.TempDir()
	mistyped := filepath.Join(root, "nested", "typo")
	emptyDir := filepath.Join(root, "empty")
	otherDir := filepath.Join(root, "documents")
	for _, dir := range []string{emptyDir, otherDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(otherDir, "notes.txt"), []byte("not a ledger"), 0o644); err != nil {
		t.Fatal(err)
	}

	for name, dir := range map[string]string{"missing directory": mistyped, "empty directory": emptyDir, "directory of something else": otherDir} {
		t.Run(name, func(t *testing.T) {
			before := treeOfListTest(t, root)

			result := runHoardBinary(t, "list", "--store", dir, "--json")

			if result.exitCode != 1 || result.stdout != "" {
				t.Fatalf("exit %d, stdout %q, want exit 1 and nothing on stdout", result.exitCode, result.stdout)
			}
			if !strings.Contains(result.stderr, "hoard list:") || !strings.Contains(result.stderr, dir) {
				t.Errorf("stderr does not say what was wrong with %s: %q", dir, result.stderr)
			}
			if after := treeOfListTest(t, root); !slices.Equal(before, after) {
				t.Errorf("the failed listing changed the tree:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
	if _, err := os.Stat(mistyped); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the mistyped store directory exists after the listing: %v", err)
	}
}

func TestListExitsTwoWhenTheCommandLineCannotBeRead(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	cases := map[string]struct {
		args    []string
		mention string
	}{
		"no flags":             {[]string{"list"}, "--store is required"},
		"no store":             {[]string{"list", "--json"}, "--store is required"},
		"empty store":          {[]string{"list", "--store", "", "--json"}, "--store is required"},
		"no json":              {[]string{"list", "--store", storeDir}, "--json is required"},
		"json switched off":    {[]string{"list", "--store", storeDir, "--json=false"}, "--json is required"},
		"stray argument":       {[]string{"list", "--store", storeDir, "--json", "extra"}, "unexpected arguments"},
		"unknown flag":         {[]string{"list", "--store", storeDir, "--json", "--nope"}, "flag provided but not defined"},
		"store without value":  {[]string{"list", "--json", "--store"}, "usage: hoard list"},
		"stray argument first": {[]string{"list", "extra", "--store", storeDir, "--json"}, "unexpected arguments"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			result := runHoardBinary(t, c.args...)
			if result.exitCode != 2 {
				t.Fatalf("exit %d, want 2 (stderr %q)", result.exitCode, result.stderr)
			}
			if !strings.Contains(result.stderr, "usage: hoard list") || !strings.Contains(result.stderr, c.mention) {
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

func TestListHelpIsNotAnErrorAndTheTopLevelUsageNamesTheCommand(t *testing.T) {
	help := runHoardBinary(t, "list", "-h")
	if help.exitCode != 0 {
		t.Fatalf("list -h exited %d", help.exitCode)
	}
	for _, want := range []string{"usage: hoard list --store <dir> --json", "-store string", "-json"} {
		if !strings.Contains(help.stdout+help.stderr, want) {
			t.Errorf("list -h does not mention %q:\n%s%s", want, help.stdout, help.stderr)
		}
	}

	top := runHoardBinary(t, "help")
	if top.exitCode != 0 || !strings.Contains(top.stdout, "  list ") {
		t.Errorf("hoard help (exit %d) does not list the list command:\n%s", top.exitCode, top.stdout)
	}
}
