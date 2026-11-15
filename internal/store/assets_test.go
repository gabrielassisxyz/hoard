package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAssetExtensionFollowsTheTypeThenTheAddress(t *testing.T) {
	cases := []struct {
		name, contentType, address, want string
	}{
		{"png", "image/png", "http://a.test/x", ".png"},
		{"jpeg", "image/jpeg", "http://a.test/x", ".jpg"},
		{"gif", "image/gif", "http://a.test/x", ".gif"},
		{"webp", "image/webp", "http://a.test/x", ".webp"},
		{"svg", "image/svg+xml", "http://a.test/x", ".svg"},
		{"avif", "image/avif", "http://a.test/x", ".avif"},
		{"a parameter on the type", "image/png; charset=binary", "http://a.test/x", ".png"},
		{"the type wins over the address", "image/png", "http://a.test/x.gif", ".png"},
		{"another type, a known extension", "image/x-icon", "http://a.test/p/pic.JPEG?v=2", ".jpeg"},
		{"another type, an unknown extension", "image/x-icon", "http://a.test/favicon.ico", ".img"},
		{"another type, no extension", "image/bmp", "http://a.test/pic", ".img"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AssetExtension(c.contentType, c.address); got != c.want {
				t.Errorf("AssetExtension(%q, %q) = %q, want %q", c.contentType, c.address, got, c.want)
			}
		})
	}
}

func TestAnImageIsKeptAsItArrivedUnderItsNameAndExtension(t *testing.T) {
	s := openScratchStore(t)
	body := []byte("\x89PNG synthetic image bytes \x00\xff")
	ref, err := s.AssetLedger().PutBody(body, "http://a.test/p", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(s.dir, "assets", ref.SHA256[:2], hexSHA256OfBytes(body)+".png")
	onDisk, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("no file at %s: %v", want, err)
	}
	if !bytes.Equal(onDisk, body) {
		t.Errorf("the file holds %q, want the bytes as they arrived", onDisk)
	}
	if files := listFilesUnder(t, filepath.Join(s.dir, "raw")); len(files) != 0 {
		t.Errorf("an image left %v under raw/", files)
	}
}

func TestAnImageRowBecomesHeldOnlyOnceItsFileIsThere(t *testing.T) {
	s := openScratchStore(t)
	assets := s.AssetLedger()
	address := "http://a.test/p.png"
	if err := assets.AddPending([]string{address}); err != nil {
		t.Fatal(err)
	}
	ref := BodyRef{SHA256: hexSHA256OfBytes([]byte("never written")), Size: 13}
	err := assets.MarkOK(address, Outcome{ContentType: "image/png", Body: ref})
	if err == nil {
		t.Fatal("MarkOK accepted a row whose file is not in the store")
	}
	if row, _, _ := assets.Page(address); row.State != StatePending {
		t.Errorf("state = %q after a refused MarkOK, want pending", row.State)
	}

	ref, err = assets.PutBody([]byte("written"), address, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if err := assets.MarkOK(address, Outcome{ContentType: "image/png", Body: ref}); err != nil {
		t.Fatalf("MarkOK after the file was written: %v", err)
	}
	if err := assets.MarkOK(address, Outcome{ContentType: "image/png", Body: ref}); !errors.Is(err, ErrPageNotPending) {
		t.Errorf("a second MarkOK returned %v, want ErrPageNotPending", err)
	}
}

func TestAssetsAndPagesNeverShareRows(t *testing.T) {
	s := openScratchStore(t)
	if err := s.AssetLedger().AddPending([]string{"http://a.test/p.png"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPending([]string{"http://a.test/page"}); err != nil {
		t.Fatal(err)
	}
	pages, _ := s.Pages()
	assets, _ := s.AssetLedger().Pages()
	if len(pages) != 1 || pages[0].Address != "http://a.test/page" {
		t.Errorf("pages = %v, want only the page", pages)
	}
	if len(assets) != 1 || assets[0].Address != "http://a.test/p.png" {
		t.Errorf("assets = %v, want only the image", assets)
	}
	if counts, _ := s.Counts(); counts.Pending != 1 {
		t.Errorf("page counts = %+v, want one pending page", counts)
	}
	if counts, _ := s.AssetLedger().Counts(); counts.Pending != 1 {
		t.Errorf("asset counts = %+v, want one pending image", counts)
	}
}

// The schema of the store before the assets table existed, written out here and not taken
// from the package, so that the test keeps describing a store that was made by an older
// version even when the package's own schema moves on.
const previousVersionSchema = `
CREATE TABLE pages (
    address       TEXT PRIMARY KEY,
    final_address TEXT,
    http_status   INTEGER,
    fetched_at    TEXT,
    sha256        TEXT,
    size_bytes    INTEGER,
    content_type  TEXT,
    attempts      INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error    TEXT,
    state         TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'ok', 'failed')),
    CHECK (state <> 'ok' OR (sha256 IS NOT NULL AND length(sha256) = 64 AND size_bytes IS NOT NULL))
) STRICT`

func TestOpeningAnOlderStoreAddsTheAssetsTableAndChangesNoPage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "old-store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sha := hexSHA256OfBytes([]byte("old body"))
	oldRow := []any{"http://a.test/old", "http://a.test/old2", 200, "2026-01-02T03:04:05Z", sha, 8, "text/html", 2, nil, "ok"}
	rowQuery := `SELECT address, final_address, http_status, fetched_at, sha256, size_bytes, content_type, attempts, last_error, state FROM pages`

	old, err := sql.Open("sqlite", filepath.Join(dir, ledgerFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(previousVersionSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO pages VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, oldRow...); err != nil {
		t.Fatal(err)
	}
	before := readRow(t, old, rowQuery)
	old.Close()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open on a store made by the previous version: %v", err)
	}
	defer s.Close()
	if got := readRow(t, s.db, rowQuery); got != before {
		t.Errorf("the page row changed:\n before %s\n after  %s", before, got)
	}
	if assets, err := s.AssetLedger().Pages(); err != nil || len(assets) != 0 {
		t.Errorf("assets table: rows %v, err %v; want an empty table that exists", assets, err)
	}
}

func readRow(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	values := make([]any, 10)
	targets := make([]any, len(values))
	for i := range values {
		targets[i] = &values[i]
	}
	if err := db.QueryRow(query).Scan(targets...); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(values...)
}
