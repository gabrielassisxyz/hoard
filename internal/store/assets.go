package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	pagesTable    = "pages"
	assetsTable   = "assets"
	assetsDirName = "assets"
	// What a file is called when neither the answer's type nor the address says what kind of
	// image it is.
	unknownImageExtension = ".img"
)

// Ledger is one of the store's two tables of addresses: the pages, and the images that live
// apart from them. Both have the same columns and the same rules; only the table and the
// way a body is kept differ, so the fetcher can fill either without knowing which.
type Ledger struct {
	s     *Store
	table string
}

// PageLedger is the table of pages, whose bodies are kept compressed under raw/.
func (s *Store) PageLedger() *Ledger { return &Ledger{s: s, table: pagesTable} }

// AssetLedger is the table of images, kept as they arrived under assets/.
func (s *Store) AssetLedger() *Ledger { return &Ledger{s: s, table: assetsTable} }

func (l *Ledger) isAssets() bool { return l.table == assetsTable }

// PutBody keeps body for the address that asked for it and returns its name. For an image
// the type and the address decide the file's extension, so they are needed here and again
// by MarkOK, which looks for the file.
func (l *Ledger) PutBody(body []byte, address, contentType string) (BodyRef, error) {
	if !l.isAssets() {
		return l.s.PutBody(body)
	}
	sum := sha256.Sum256(body)
	ref := BodyRef{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	file := l.s.assetPath(ref.SHA256, address, contentType)
	if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() {
		return ref, nil
	}
	if err := writeFileAtomically(file, body); err != nil {
		return BodyRef{}, fmt.Errorf("store image %s: %w", ref.SHA256, err)
	}
	return ref, nil
}

func (l *Ledger) bodyPath(address string, outcome Outcome) string {
	if !l.isAssets() {
		return l.s.bodyPath(outcome.Body.SHA256)
	}
	return l.s.assetPath(outcome.Body.SHA256, address, outcome.ContentType)
}

// AssetPath is the file that holds a held image's bytes, as an absolute path.
func (s *Store) AssetPath(page Page) string {
	return s.assetPath(page.SHA256, page.Address, page.ContentType)
}

func (s *Store) assetPath(sha256Hex, address, contentType string) string {
	return filepath.Join(s.dir, assetsDirName, sha256Hex[:2], sha256Hex+AssetExtension(contentType, address))
}

var imageExtensionsByType = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/gif":     ".gif",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
	"image/avif":    ".avif",
}

var knownImageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true, ".avif": true,
}

// AssetExtension is the extension an image is stored under: the one its Content-Type names,
// or for another image type the one on the address's path when that is a kind of image this
// store knows, and ".img" otherwise.
func AssetExtension(contentType, address string) string {
	if extension, ok := imageExtensionsByType[mediaTypeOf(contentType)]; ok {
		return extension
	}
	if parsed, err := url.Parse(address); err == nil {
		if extension := strings.ToLower(path.Ext(parsed.Path)); knownImageExtensions[extension] {
			return extension
		}
	}
	return unknownImageExtension
}

// IsImageType is true for an answer's Content-Type that names an image.
func IsImageType(contentType string) bool {
	return strings.HasPrefix(mediaTypeOf(contentType), "image/")
}

func mediaTypeOf(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return mediaType
}
