package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// treeSnapshot lists everything under root, directories included, with a hash of each
// file's bytes, so that a test can tell "nothing was created" from "nothing was changed".
func treeSnapshot(t *testing.T, root string) []string {
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
		entries = append(entries, rel+" "+hexSHA256OfBytes(content))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return entries
}

func TestOpenExistingReportsNoStoreAndCreatesNothing(t *testing.T) {
	scenarios := map[string]func(t *testing.T, root string) string{
		"directory that does not exist": func(t *testing.T, root string) string {
			return filepath.Join(root, "nested", "typo")
		},
		"empty directory": func(t *testing.T, root string) string {
			dir := filepath.Join(root, "empty")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"bodies without a ledger": func(t *testing.T, root string) string {
			dir := filepath.Join(root, "bodies-only")
			if err := os.MkdirAll(filepath.Join(dir, "raw", "ab"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "raw", "ab", "left-over.zst"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"unrelated files": func(t *testing.T, root string) string {
			dir := filepath.Join(root, "documents")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a ledger"), 0o644); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"a file where the directory should be": func(t *testing.T, root string) string {
			file := filepath.Join(root, "just-a-file")
			if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			return file
		},
	}
	for name, prepare := range scenarios {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := prepare(t, root)
			before := treeSnapshot(t, root)

			s, err := OpenExisting(dir)

			if !errors.Is(err, ErrNoStore) {
				t.Fatalf("OpenExisting(%q) error = %v, want one wrapping ErrNoStore", dir, err)
			}
			if s != nil {
				t.Errorf("OpenExisting returned a store together with an error")
			}
			if !strings.Contains(err.Error(), dir) {
				t.Errorf("the error does not name the directory %q: %v", dir, err)
			}
			if after := treeSnapshot(t, root); !slices.Equal(before, after) {
				t.Errorf("OpenExisting changed the directory tree:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

func TestOpenExistingOpensAStoreWithoutChangingIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	seed, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const address = "https://site.test/held"
	if err := seed.AddPending([]string{address}); err != nil {
		t.Fatal(err)
	}
	ref, err := seed.PutBody([]byte("<p>held</p>"))
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.MarkOK(address, Outcome{Body: ref, HTTPStatus: 200}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	before := treeSnapshot(t, dir)

	s, err := OpenExisting(dir)
	if err != nil {
		t.Fatalf("OpenExisting on a store: %v", err)
	}
	page, found, err := s.Page(address)
	if err != nil || !found || page.State != StateOK || page.SHA256 != ref.SHA256 {
		t.Errorf("Page = %+v, found %v, err %v", page, found, err)
	}
	if body, err := s.ReadBody(ref.SHA256); err != nil || string(body) != "<p>held</p>" {
		t.Errorf("ReadBody = %q, %v", body, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if after := treeSnapshot(t, dir); !slices.Equal(before, after) {
		t.Errorf("opening and closing an existing store changed it:\nbefore %v\nafter  %v", before, after)
	}
}
