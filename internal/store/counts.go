package store

import "fmt"

// Counts is how many ledger rows are in each state.
type Counts struct {
	Held    int // ok: the body is in the store
	Failed  int
	Pending int
}

// Total is every address the ledger knows.
func (c Counts) Total() int {
	return c.Held + c.Failed + c.Pending
}

// Counts reads the ledger's size by state without loading its rows.
func (s *Store) Counts() (Counts, error) {
	rows, err := s.db.Query(`SELECT state, COUNT(*) FROM pages GROUP BY state`)
	if err != nil {
		return Counts{}, fmt.Errorf("count the ledger: %w", err)
	}
	defer rows.Close()
	var counts Counts
	for rows.Next() {
		var state string
		var rowsInState int
		if err := rows.Scan(&state, &rowsInState); err != nil {
			return Counts{}, fmt.Errorf("count the ledger: %w", err)
		}
		switch state {
		case StateOK:
			counts.Held = rowsInState
		case StateFailed:
			counts.Failed = rowsInState
		case StatePending:
			counts.Pending = rowsInState
		}
	}
	return counts, rows.Err()
}
