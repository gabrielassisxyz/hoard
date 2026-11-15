// Package store is the raw capture of a run: every response body, compressed and named by
// its SHA-256, plus a ledger that records what became of each requested address.
//
// The store is a directory. It holds ledger.db, the page bodies under
// raw/<first two hex digits>/<sha256>.zst and the images under
// assets/<first two hex digits>/<sha256><extension>. Nothing here parses or rewrites a
// body; what went in is what comes out.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
	// Pure Go, so the binary builds with CGO_ENABLED=0.
	_ "modernc.org/sqlite"
)

const (
	ledgerFileName = "ledger.db"
	rawDirName     = "raw"
)

// Store is an open store directory. It is safe for concurrent use.
type Store struct {
	dir string
	db  *sql.DB
	enc *zstd.Encoder
	dec *zstd.Decoder
}

// Open opens the store in dir, creating the directory and an empty ledger when they do not
// exist yet.
func Open(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve store directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, rawDirName), 0o755); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}
	db, err := openLedger(filepath.Join(abs, ledgerFileName))
	if err != nil {
		return nil, err
	}
	// A zero-length body is a valid capture, and without zero frames the encoder would write
	// an empty file that no decoder accepts.
	enc, err := zstd.NewWriter(nil, zstd.WithZeroFrames(true))
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create compressor: %w", err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		db.Close()
		enc.Close()
		return nil, fmt.Errorf("create decompressor: %w", err)
	}
	return &Store{dir: abs, db: db, enc: enc, dec: dec}, nil
}

// Close releases the ledger and the codecs. Bodies already written stay where they are.
func (s *Store) Close() error {
	s.dec.Close()
	encErr := s.enc.Close()
	if err := s.db.Close(); err != nil {
		return err
	}
	return encErr
}

func openLedger(path string) (*sql.DB, error) {
	// A file: URI keeps every character of the path literal; a bare path would make a
	// store directory containing "?" be read as the start of the options.
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout(5000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	// SQLite admits one writer at a time. One connection makes the callers' writes queue up
	// in the pool instead of racing each other for the file lock.
	db.SetMaxOpenConns(1)
	for _, table := range []string{pagesTable, assetsTable} {
		if _, err := db.Exec(fmt.Sprintf(tableSchema, table)); err != nil {
			db.Close()
			return nil, fmt.Errorf("prepare ledger: %w", err)
		}
	}
	return db, nil
}

// The methods below are the page ledger's, so that what only deals with pages keeps asking
// the store and never has to know there is another table.

func (s *Store) AddPending(addresses []string) error { return s.PageLedger().AddPending(addresses) }

func (s *Store) Page(address string) (Page, bool, error) { return s.PageLedger().Page(address) }

func (s *Store) Pages() ([]Page, error) { return s.PageLedger().Pages() }

func (s *Store) NoteAttempt(address string, outcome Outcome) error {
	return s.PageLedger().NoteAttempt(address, outcome)
}

func (s *Store) MarkFailed(address string, outcome Outcome) error {
	return s.PageLedger().MarkFailed(address, outcome)
}

func (s *Store) MarkRejected(address, reason string) error {
	return s.PageLedger().MarkRejected(address, reason)
}

func (s *Store) MarkOK(address string, outcome Outcome) error {
	return s.PageLedger().MarkOK(address, outcome)
}

func (s *Store) Counts() (Counts, error) { return s.PageLedger().Counts() }
