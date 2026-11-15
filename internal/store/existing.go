package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// ErrNoStore is what OpenExisting wraps when the directory it was given holds no ledger.
var ErrNoStore = errors.New("no store found")

// OpenExisting opens the store in dir like Open, but only when one is already there. A
// command that only reads would otherwise turn a mistyped directory into an empty store,
// and the typo would go unnoticed until the next run found nothing in it.
//
// When dir, or the ledger inside it, is missing the error wraps ErrNoStore and nothing has
// been created.
func OpenExisting(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve store directory: %w", err)
	}
	if _, err := os.Stat(filepath.Join(abs, ledgerFileName)); err != nil {
		// ENOTDIR is a dir that is a file: there is no ledger under it either, and saying so
		// is more useful than a bare "not a directory".
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return nil, fmt.Errorf("%w: %s holds no %s", ErrNoStore, dir, ledgerFileName)
		}
		return nil, fmt.Errorf("look for the ledger in %s: %w", dir, err)
	}
	return Open(abs)
}
