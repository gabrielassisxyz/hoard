package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func openScratchStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "scratch-store"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sha256HexOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// decodeBodyFileDirectly reads a body file without going through the store, so a test that
// uses it checks the on-disk format and not the store's own reader.
func decodeBodyFileDirectly(t *testing.T, path string) []byte {
	t.Helper()
	compressed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatalf("zstd reader: %v", err)
	}
	defer dec.Close()
	plain, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		t.Fatalf("%s is not a zstd stream: %v", path, err)
	}
	return plain
}

func listFilesUnder(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}

func TestPutBodyRoundTripsThroughCompression(t *testing.T) {
	everyByte := make([]byte, 0, 256*4)
	for range 4 {
		for b := range 256 {
			everyByte = append(everyByte, byte(b))
		}
	}
	noise := make([]byte, 300<<10)
	rand.New(rand.NewSource(1)).Read(noise)

	bodies := map[string][]byte{
		"empty":                {},
		"one byte":             {'x'},
		"crlf and no final lf": []byte("<html>\r\n<body>caf\xc3\xa9 \xff\xfe</body>\r\n</html>"),
		"every byte value":     everyByte,
		"incompressible":       noise,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			s := openScratchStore(t)
			ref, err := s.PutBody(body)
			if err != nil {
				t.Fatalf("PutBody: %v", err)
			}
			if want := sha256HexOf(body); ref.SHA256 != want {
				t.Fatalf("name = %s, want the SHA-256 of the body, %s", ref.SHA256, want)
			}
			if ref.Size != int64(len(body)) {
				t.Fatalf("size = %d, want %d", ref.Size, len(body))
			}
			onDisk := filepath.Join(s.dir, "raw", ref.SHA256[:2], ref.SHA256+".zst")
			if got := decodeBodyFileDirectly(t, onDisk); !bytes.Equal(got, body) {
				t.Fatalf("file at %s decompresses to %d bytes that differ from the %d stored", onDisk, len(got), len(body))
			}
			got, err := s.ReadBody(ref.SHA256)
			if err != nil {
				t.Fatalf("ReadBody: %v", err)
			}
			if !bytes.Equal(got, body) {
				t.Fatalf("ReadBody returned %d bytes that differ from the %d stored", len(got), len(body))
			}
		})
	}
}

func TestPutBodyActuallyCompresses(t *testing.T) {
	s := openScratchStore(t)
	body := bytes.Repeat([]byte("<p>the same paragraph again</p>\n"), 4096)
	ref, err := s.PutBody(body)
	if err != nil {
		t.Fatalf("PutBody: %v", err)
	}
	info, err := os.Stat(s.bodyPath(ref.SHA256))
	if err != nil {
		t.Fatalf("stat body: %v", err)
	}
	if info.Size() >= int64(len(body))/10 {
		t.Fatalf("a %d byte repetitive body took %d bytes on disk; it was not compressed", len(body), info.Size())
	}
}

func TestPutBodyLeavesNoTemporaryFileBehind(t *testing.T) {
	s := openScratchStore(t)
	for _, body := range []string{"one", "two", "one", "three", ""} {
		if _, err := s.PutBody([]byte(body)); err != nil {
			t.Fatalf("PutBody(%q): %v", body, err)
		}
	}
	files := listFilesUnder(t, filepath.Join(s.dir, "raw"))
	if len(files) != 4 {
		t.Fatalf("%d body files for 4 distinct bodies: %v", len(files), files)
	}
	for _, file := range files {
		if !strings.HasSuffix(file, ".zst") || strings.Contains(filepath.Base(file), tempBodyPrefix) {
			t.Errorf("%s is not a finished body file", file)
		}
	}
}

func TestPutBodyCleansUpWhenItCannotRenameIntoPlace(t *testing.T) {
	s := openScratchStore(t)
	body := []byte("a body that cannot land")
	blocker := s.bodyPath(sha256HexOf(body))
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutBody(body); err == nil {
		t.Fatal("PutBody succeeded although a directory sits where the body file belongs")
	}
	for _, entry := range listFilesUnder(t, filepath.Dir(blocker)) {
		t.Errorf("left behind after a failed write: %s", entry)
	}
}

func TestReadBodyRefusesNamesThatAreNotHashes(t *testing.T) {
	s := openScratchStore(t)
	for _, name := range []string{"", "abc", "../../ledger.db", strings.Repeat("g", 64), strings.Repeat("A", 64)} {
		if _, err := s.ReadBody(name); err == nil {
			t.Errorf("ReadBody(%q) succeeded", name)
		}
	}
}

func TestMarkOKNeedsAStoredBodyWithItsHashAndSize(t *testing.T) {
	s := openScratchStore(t)
	if err := s.AddPending([]string{"https://site.test/a"}); err != nil {
		t.Fatal(err)
	}
	const address = "https://site.test/a"
	absent := BodyRef{SHA256: sha256HexOf([]byte("never stored")), Size: 12}
	ref, err := s.PutBody([]byte("<p>held</p>"))
	if err != nil {
		t.Fatal(err)
	}
	// A name of the right length but not a hash, with a file where it would point: only the
	// check on the name itself can refuse it, since the file exists and the length is right.
	notAHash := strings.Repeat("g", 64)
	decoy := filepath.Join(s.dir, "raw", notAHash[:2], notAHash+bodySuffix)
	if err := os.MkdirAll(filepath.Dir(decoy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decoy, []byte("not a body"), 0o644); err != nil {
		t.Fatal(err)
	}

	refused := map[string]Outcome{
		"no body at all":                 {},
		"hash without a file":            {Body: absent},
		"hash that is too short":         {Body: BodyRef{SHA256: "xyz", Size: 1}},
		"hash that is not hexadecimal":   {Body: BodyRef{SHA256: notAHash, Size: 1}},
		"negative size on a stored body": {Body: BodyRef{SHA256: ref.SHA256, Size: -1}},
		"hash missing, size given":       {Body: BodyRef{Size: 12}},
	}
	for name, outcome := range refused {
		if err := s.MarkOK(address, outcome); err == nil {
			t.Errorf("%s: MarkOK succeeded", name)
		}
		page, _, _ := s.Page(address)
		if page.State != StatePending || page.SHA256 != "" || page.Attempts != 0 {
			t.Fatalf("%s: row changed to %+v", name, page)
		}
	}

	if err := s.MarkOK(address, Outcome{Body: ref, HTTPStatus: 200}); err != nil {
		t.Fatalf("MarkOK with a stored body: %v", err)
	}
	page, _, _ := s.Page(address)
	if page.State != StateOK || page.SHA256 != ref.SHA256 || page.SizeBytes != ref.Size {
		t.Fatalf("row after MarkOK = %+v, want ok with hash %s and size %d", page, ref.SHA256, ref.Size)
	}
}

func TestSchemaItselfRefusesAnOKRowWithoutHashAndSize(t *testing.T) {
	s := openScratchStore(t)
	if err := s.AddPending([]string{"https://site.test/a"}); err != nil {
		t.Fatal(err)
	}
	bypasses := map[string]string{
		"flip the state":     `UPDATE pages SET state = 'ok' WHERE address = 'https://site.test/a'`,
		"hash but no size":   `UPDATE pages SET state = 'ok', sha256 = '` + strings.Repeat("a", 64) + `' WHERE address = 'https://site.test/a'`,
		"size but no hash":   `UPDATE pages SET state = 'ok', size_bytes = 3 WHERE address = 'https://site.test/a'`,
		"short hash":         `UPDATE pages SET state = 'ok', sha256 = 'abc', size_bytes = 3 WHERE address = 'https://site.test/a'`,
		"insert as ok":       `INSERT INTO pages (address, state) VALUES ('https://site.test/b', 'ok')`,
		"state out of vocab": `UPDATE pages SET state = 'done' WHERE address = 'https://site.test/a'`,
	}
	for name, statement := range bypasses {
		if _, err := s.db.Exec(statement); err == nil {
			t.Errorf("%s: the ledger accepted %s", name, statement)
		}
	}
}

func TestAddPendingKeepsTheRowOfAnAddressAlreadyInTheLedger(t *testing.T) {
	s := openScratchStore(t)
	if err := s.AddPending([]string{"https://site.test/a", "https://site.test/b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFailed("https://site.test/a", Outcome{HTTPStatus: 404, Err: "HTTP 404 Not Found"}); err != nil {
		t.Fatal(err)
	}
	ref, _ := s.PutBody([]byte("b"))
	if err := s.MarkOK("https://site.test/b", Outcome{Body: ref, HTTPStatus: 200}); err != nil {
		t.Fatal(err)
	}

	list := []string{"https://site.test/a", "https://site.test/b", "https://site.test/c"}
	if err := s.AddPending(list); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"https://site.test/a": StateFailed,
		"https://site.test/b": StateOK,
		"https://site.test/c": StatePending,
	}
	pages, err := s.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("%d rows for 3 addresses: %+v", len(pages), pages)
	}
	for _, page := range pages {
		if page.State != want[page.Address] {
			t.Errorf("%s is %s, want %s", page.Address, page.State, want[page.Address])
		}
	}
	if page, _, _ := s.Page("https://site.test/a"); page.HTTPStatus != 404 || page.Attempts != 1 {
		t.Errorf("the failed row was rewritten: %+v", page)
	}
}

func TestASettledRowCannotBeSettledAgain(t *testing.T) {
	s := openScratchStore(t)
	const address = "https://site.test/a"
	if err := s.AddPending([]string{address}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFailed(address, Outcome{Err: "gone"}); err != nil {
		t.Fatal(err)
	}
	ref, _ := s.PutBody([]byte("late"))
	attempts := map[string]error{
		"NoteAttempt":        s.NoteAttempt(address, Outcome{Err: "again"}),
		"MarkFailed":         s.MarkFailed(address, Outcome{Err: "again"}),
		"MarkOK":             s.MarkOK(address, Outcome{Body: ref}),
		"MarkRejected":       s.MarkRejected(address, "again"),
		"an unknown address": s.MarkFailed("https://site.test/never-added", Outcome{}),
	}
	for name, err := range attempts {
		if !errors.Is(err, ErrPageNotPending) {
			t.Errorf("%s returned %v, want ErrPageNotPending", name, err)
		}
	}
	if page, _, _ := s.Page(address); page.State != StateFailed || page.LastError != "gone" || page.Attempts != 1 {
		t.Errorf("row changed after settling: %+v", page)
	}
}

func TestEveryAttemptIsCountedAndRejectionIsNot(t *testing.T) {
	s := openScratchStore(t)
	const address = "https://site.test/flaky"
	if err := s.AddPending([]string{address, "not an address"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{500, 502} {
		if err := s.NoteAttempt(address, Outcome{HTTPStatus: status, Err: "HTTP error"}); err != nil {
			t.Fatal(err)
		}
	}
	page, _, _ := s.Page(address)
	if page.State != StatePending || page.Attempts != 2 || page.HTTPStatus != 502 || page.LastError != "HTTP error" {
		t.Fatalf("after two noted attempts: %+v", page)
	}

	ref, _ := s.PutBody([]byte("finally"))
	if err := s.MarkOK(address, Outcome{Body: ref, HTTPStatus: 200, FinalAddress: address + "?final"}); err != nil {
		t.Fatal(err)
	}
	page, _, _ = s.Page(address)
	if page.Attempts != 3 || page.LastError != "" || page.FinalAddress != address+"?final" {
		t.Fatalf("after the third attempt succeeded: %+v", page)
	}

	if err := s.MarkRejected("not an address", "invalid address"); err != nil {
		t.Fatal(err)
	}
	rejected, _, _ := s.Page("not an address")
	if rejected.State != StateFailed || rejected.Attempts != 0 || rejected.LastError != "invalid address" {
		t.Fatalf("rejected row: %+v", rejected)
	}
}

func TestLedgerSurvivesReopeningTheStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "store")
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const address = "https://site.test/kept"
	if err := first.AddPending([]string{address}); err != nil {
		t.Fatal(err)
	}
	ref, _ := first.PutBody([]byte("kept"))
	fetched := time.Date(2026, 11, 14, 19, 41, 2, 0, time.UTC)
	outcome := Outcome{Body: ref, HTTPStatus: 200, FetchedAt: fetched, ContentType: "text/html; charset=utf-8"}
	if err := first.MarkOK(address, outcome); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	page, found, err := second.Page(address)
	if err != nil || !found {
		t.Fatalf("Page after reopen: found=%v err=%v", found, err)
	}
	if !page.FetchedAt.Equal(fetched) || page.FetchedAt.Location() != time.UTC {
		t.Errorf("FetchedAt = %v, want %v in UTC", page.FetchedAt, fetched)
	}
	if page.ContentType != outcome.ContentType || page.SHA256 != ref.SHA256 {
		t.Errorf("row after reopen: %+v", page)
	}
	if body, err := second.ReadBody(ref.SHA256); err != nil || string(body) != "kept" {
		t.Errorf("body after reopen = %q, %v", body, err)
	}
}

func TestOpenCreatesTheStoreLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does", "not", "exist")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, path := range []string{filepath.Join(dir, "ledger.db"), filepath.Join(dir, "raw")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("a fresh store lacks %s: %v", path, err)
		}
	}
}

func TestOpenAcceptsADirectoryNameThatLooksLikeAURLOption(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "what?_pragma=nope#frag %20")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}
	defer s.Close()
	if _, err := os.Stat(filepath.Join(dir, "ledger.db")); err != nil {
		t.Fatalf("the ledger is not inside the named directory: %v", err)
	}
}
