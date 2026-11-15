package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCountsSaysHowManyRowsAreInEachState(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	empty, err := st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if empty != (Counts{}) || empty.Total() != 0 {
		t.Errorf("Counts of an empty ledger = %+v", empty)
	}

	addresses := []string{"https://site.invalid/a", "https://site.invalid/b", "https://site.invalid/c", "https://site.invalid/d", "https://site.invalid/e", "https://site.invalid/f"}
	if err := st.AddPending(addresses); err != nil {
		t.Fatal(err)
	}
	for _, held := range addresses[:3] {
		ref, err := st.PutBody([]byte("body of " + held))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.MarkOK(held, Outcome{HTTPStatus: 200, FetchedAt: time.Now(), Body: ref}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.MarkFailed(addresses[3], Outcome{HTTPStatus: 404, Err: "HTTP 404 Not Found"}); err != nil {
		t.Fatal(err)
	}

	got, err := st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	want := Counts{Held: 3, Failed: 1, Pending: 2}
	if got != want {
		t.Errorf("Counts() = %+v, want %+v", got, want)
	}
	if got.Total() != len(addresses) {
		t.Errorf("Total() = %d, want %d", got.Total(), len(addresses))
	}
}
