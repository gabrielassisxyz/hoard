package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const (
	bodySuffix     = ".zst"
	tempBodyPrefix = ".incoming-"
)

// BodyRef names a stored body: the SHA-256 of the bytes exactly as they were handed to
// PutBody, and how many there were.
type BodyRef struct {
	SHA256 string
	Size   int64
}

// PutBody compresses body into the store under its SHA-256 and returns that name.
//
// The compressed bytes go to a temporary file in the directory they are destined for and
// are renamed into place, so a body file is either absent or complete. Two addresses that
// serve the same bytes share one file.
func (s *Store) PutBody(body []byte) (BodyRef, error) {
	sum := sha256.Sum256(body)
	ref := BodyRef{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	path := s.bodyPath(ref.SHA256)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return ref, nil
	}
	if err := writeFileAtomically(path, s.enc.EncodeAll(body, nil)); err != nil {
		return BodyRef{}, fmt.Errorf("store body %s: %w", ref.SHA256, err)
	}
	return ref, nil
}

// ReadBody returns the bytes that PutBody was given for the body named sha256Hex.
func (s *Store) ReadBody(sha256Hex string) ([]byte, error) {
	if !isSHA256Hex(sha256Hex) {
		return nil, fmt.Errorf("not a SHA-256 name: %q", sha256Hex)
	}
	compressed, err := os.ReadFile(s.bodyPath(sha256Hex))
	if err != nil {
		return nil, err
	}
	return s.dec.DecodeAll(compressed, nil)
}

func (s *Store) bodyPath(sha256Hex string) string {
	return filepath.Join(s.dir, rawDirName, sha256Hex[:2], sha256Hex+bodySuffix)
}

func writeFileAtomically(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, tempBodyPrefix+"*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	// Without the sync a power cut can leave the rename durable and the bytes not, and the
	// ledger would then name a body that reads back empty.
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	syncDirectoryBestEffort(dir)
	return nil
}

// Some filesystems refuse to sync a directory; the file itself is already on disk by now,
// so a refusal here is not worth failing the capture over.
func syncDirectoryBestEffort(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	d.Sync()
	d.Close()
}

func isSHA256Hex(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
