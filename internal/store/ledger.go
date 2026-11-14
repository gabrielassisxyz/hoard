package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// States of a ledger row. A row starts pending and leaves that state exactly once.
const (
	StatePending = "pending"
	StateOK      = "ok"
	StateFailed  = "failed"
)

// ErrPageNotPending is returned when a row that already reached ok or failed is asked to
// change again. Settled rows are how a later run knows what it need not fetch.
var ErrPageNotPending = errors.New("page is not pending")

// The CHECK on state is the last line of defence for the rule that a row is ok only with
// the name and size of a body: it holds even for a writer that bypasses this package.
// Each operand is tested for NULL on its own because a CHECK passes when it evaluates to
// NULL, and length(NULL) = 64 is NULL, not false.
const ledgerSchema = `
CREATE TABLE IF NOT EXISTS pages (
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

const pageColumns = `address, final_address, http_status, fetched_at, sha256, size_bytes,
    content_type, attempts, last_error, state`

// Page is one ledger row. Fields a request never produced are left at their zero value.
type Page struct {
	// Address is the address as it was requested; it is the row's key.
	Address string
	// FinalAddress is the address the answer finally came from. It differs from Address when
	// the site redirected, and is kept because later link resolution needs the address a
	// page is actually served from.
	FinalAddress string
	HTTPStatus   int
	FetchedAt    time.Time
	SHA256       string
	SizeBytes    int64
	ContentType  string
	Attempts     int
	LastError    string
	State        string
}

// Outcome is what one attempt on an address learned.
type Outcome struct {
	FinalAddress string
	HTTPStatus   int // zero when no answer arrived
	FetchedAt    time.Time
	ContentType  string
	Body         BodyRef // read by MarkOK only
	Err          string  // empty when the attempt had no error to report
}

// AddPending gives every address a row. An address that already has one keeps it, in
// whatever state it reached, so listing an address again never reopens it.
func (s *Store) AddPending(addresses []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("add addresses to the ledger: %w", err)
	}
	defer tx.Rollback()
	insert, err := tx.Prepare(`INSERT OR IGNORE INTO pages (address) VALUES (?)`)
	if err != nil {
		return fmt.Errorf("add addresses to the ledger: %w", err)
	}
	defer insert.Close()
	for _, address := range addresses {
		if _, err := insert.Exec(address); err != nil {
			return fmt.Errorf("add %q to the ledger: %w", address, err)
		}
	}
	return tx.Commit()
}

// Page returns the row for address, and false when there is none.
func (s *Store) Page(address string) (Page, bool, error) {
	row := s.db.QueryRow(`SELECT `+pageColumns+` FROM pages WHERE address = ?`, address)
	page, err := scanPage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Page{}, false, nil
	}
	if err != nil {
		return Page{}, false, fmt.Errorf("read %q from the ledger: %w", address, err)
	}
	return page, true, nil
}

// Pages returns every row, ordered by address.
func (s *Store) Pages() ([]Page, error) {
	rows, err := s.db.Query(`SELECT ` + pageColumns + ` FROM pages ORDER BY address`)
	if err != nil {
		return nil, fmt.Errorf("read the ledger: %w", err)
	}
	defer rows.Close()
	var pages []Page
	for rows.Next() {
		page, err := scanPage(rows)
		if err != nil {
			return nil, fmt.Errorf("read the ledger: %w", err)
		}
		pages = append(pages, page)
	}
	return pages, rows.Err()
}

// NoteAttempt records an attempt that did not settle the address: it stays pending, with
// one more attempt counted and the attempt's facts on the row.
func (s *Store) NoteAttempt(address string, outcome Outcome) error {
	return s.settle(address, StatePending, outcome)
}

// MarkFailed settles the address as failed, counting the attempt that did it.
func (s *Store) MarkFailed(address string, outcome Outcome) error {
	return s.settle(address, StateFailed, outcome)
}

// MarkRejected settles an address that was never requested because it cannot be, such as
// text that is not a web address. No attempt is counted, since none was made.
func (s *Store) MarkRejected(address, reason string) error {
	return s.updatePending(address,
		`UPDATE pages SET state = 'failed', last_error = ? WHERE address = ? AND state = 'pending'`,
		reason, address)
}

// MarkOK settles the address as held. It refuses unless outcome names a body whose file is
// already in the store, which is what makes "body first, row second" the only order a
// caller can follow: a crash can leave a body with no row, never a row with no body.
func (s *Store) MarkOK(address string, outcome Outcome) error {
	ref := outcome.Body
	if !isSHA256Hex(ref.SHA256) || ref.Size < 0 {
		return fmt.Errorf("mark %q ok: the outcome names no stored body", address)
	}
	if _, err := os.Stat(s.bodyPath(ref.SHA256)); err != nil {
		return fmt.Errorf("mark %q ok: body %s is not in the store: %w", address, ref.SHA256, err)
	}
	return s.updatePending(address,
		`UPDATE pages SET state = 'ok', attempts = attempts + 1, final_address = ?, http_status = ?,
		    fetched_at = ?, content_type = ?, sha256 = ?, size_bytes = ?, last_error = NULL
		 WHERE address = ? AND state = 'pending'`,
		nullableText(outcome.FinalAddress), nullableInt(outcome.HTTPStatus), nullableTime(outcome.FetchedAt),
		nullableText(outcome.ContentType), ref.SHA256, ref.Size, address)
}

func (s *Store) settle(address, state string, o Outcome) error {
	return s.updatePending(address,
		`UPDATE pages SET state = ?, attempts = attempts + 1, final_address = ?, http_status = ?,
		    fetched_at = ?, content_type = ?, last_error = ?
		 WHERE address = ? AND state = 'pending'`,
		state, nullableText(o.FinalAddress), nullableInt(o.HTTPStatus), nullableTime(o.FetchedAt),
		nullableText(o.ContentType), nullableText(o.Err), address)
}

// updatePending runs an UPDATE written to match only pending rows and reports a row that
// was missing or already settled instead of silently changing nothing.
func (s *Store) updatePending(address, query string, args ...any) error {
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("update %q in the ledger: %w", address, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update %q in the ledger: %w", address, err)
	}
	if changed != 1 {
		return fmt.Errorf("update %q in the ledger: %w", address, ErrPageNotPending)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPage(row rowScanner) (Page, error) {
	var (
		page                                                    Page
		finalAddress, fetchedAt, sha256, contentType, lastError sql.NullString
		status, size                                            sql.NullInt64
	)
	err := row.Scan(&page.Address, &finalAddress, &status, &fetchedAt, &sha256, &size,
		&contentType, &page.Attempts, &lastError, &page.State)
	if err != nil {
		return Page{}, err
	}
	page.FinalAddress = finalAddress.String
	page.HTTPStatus = int(status.Int64)
	page.SHA256 = sha256.String
	page.SizeBytes = size.Int64
	page.ContentType = contentType.String
	page.LastError = lastError.String
	if fetchedAt.Valid {
		parsed, err := time.Parse(time.RFC3339, fetchedAt.String)
		if err != nil {
			return Page{}, fmt.Errorf("fetched_at %q: %w", fetchedAt.String, err)
		}
		page.FetchedAt = parsed.UTC()
	}
	return page, nil
}

func nullableText(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullableInt(n int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(n), Valid: n != 0}
}

func nullableTime(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(time.RFC3339), Valid: true}
}
