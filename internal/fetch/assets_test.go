package fetch_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

// Synthetic images: what a server sends is only a sequence of bytes with a type, and the
// fetcher never looks inside.
var (
	syntheticPNG  = []byte("\x89PNG\r\n synthetic one \x00\x01\xff")
	syntheticJPEG = []byte("\xff\xd8\xff synthetic two \x00\x02")
	syntheticSVG  = []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><title>three</title></svg>")
)

func serveImages(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/one.png", "/same.png":
		w.Header().Set("Content-Type", "image/png")
		w.Write(syntheticPNG)
	case "/two":
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(syntheticJPEG)
	case "/three":
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(syntheticSVG)
	case "/error-page":
		writeSyntheticHTML(w, "<p>not found, said with a 200</p>")
	default:
		http.NotFound(w, r)
	}
}

func runImagesOver(t *testing.T, st *store.Store, addresses []string) fetch.RunSummary {
	t.Helper()
	return runFetchOver(t, st, addresses, func(cfg *fetch.RunConfig) { cfg.Images = true })
}

func assetFilesUnder(t *testing.T, storeDir string) []string {
	t.Helper()
	var files []string
	root := filepath.Join(storeDir, "assets")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return files
}

func TestImagesAreKeptByteForByteUnderTheirHashAndTypeAndHeld(t *testing.T) {
	site := newRecordingSite(t, serveImages)
	st, dir := openFreshStore(t)
	addresses := site.addresses("/one.png", "/two", "/three")
	if summary := runImagesOver(t, st, addresses); !summary.Complete() || summary.Held != 3 {
		t.Fatalf("summary = %v, want three held", summary)
	}
	wantFiles := map[string][]byte{
		hexSHA256OfBytes(syntheticPNG) + ".png":  syntheticPNG,
		hexSHA256OfBytes(syntheticJPEG) + ".jpg": syntheticJPEG,
		hexSHA256OfBytes(syntheticSVG) + ".svg":  syntheticSVG,
	}
	files := assetFilesUnder(t, dir)
	if len(files) != len(wantFiles) {
		t.Fatalf("files under assets/ = %v, want %d", files, len(wantFiles))
	}
	for name, want := range wantFiles {
		path := filepath.Join(dir, "assets", name[:2], name)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: err %v, bytes equal %v", path, err, bytes.Equal(got, want))
		}
	}
	assets, err := st.AssetLedger().Pages()
	if err != nil || len(assets) != 3 {
		t.Fatalf("asset rows = %v, err %v, want 3", assets, err)
	}
	for _, row := range assets {
		if row.State != store.StateOK {
			t.Errorf("%s is %s, want ok", row.Address, row.State)
		}
	}
	if pages, _ := st.Pages(); len(pages) != 0 {
		t.Errorf("images left %d rows in pages", len(pages))
	}
}

func TestTwoAddressesWithTheSameBytesShareOneFile(t *testing.T) {
	site := newRecordingSite(t, serveImages)
	st, dir := openFreshStore(t)
	summary := runImagesOver(t, st, site.addresses("/one.png", "/same.png"))
	if summary.Held != 2 {
		t.Fatalf("summary = %v, want two held", summary)
	}
	if files := assetFilesUnder(t, dir); len(files) != 1 {
		t.Errorf("files = %v, want one", files)
	}
	if assets, _ := st.AssetLedger().Pages(); len(assets) != 2 {
		t.Errorf("rows = %d, want two", len(assets))
	}
}

func TestAnAnswerThatIsNotAnImageFailsTheAddressAndIsNotAskedAgain(t *testing.T) {
	site := newRecordingSite(t, serveImages)
	st, dir := openFreshStore(t)
	address := site.address("/error-page")
	summary := runImagesOver(t, st, []string{address})
	if summary.Failed != 1 || summary.Complete() {
		t.Fatalf("summary = %v, want one failed", summary)
	}
	row, found, err := st.AssetLedger().Page(address)
	if err != nil || !found {
		t.Fatalf("no row: %v", err)
	}
	if row.State != store.StateFailed || !strings.Contains(row.LastError, "text/html") {
		t.Errorf("row = state %q, reason %q; want failed naming text/html", row.State, row.LastError)
	}
	if files := assetFilesUnder(t, dir); len(files) != 0 {
		t.Errorf("a non-image left %v", files)
	}
	runImagesOver(t, st, []string{address})
	if site.requestCount() != 1 {
		t.Errorf("the server saw %d requests over two runs, want 1", site.requestCount())
	}
}

func TestASecondRunOverTheSameListAsksForNothing(t *testing.T) {
	site := newRecordingSite(t, serveImages)
	st, _ := openFreshStore(t)
	addresses := site.addresses("/one.png", "/two", "/error-page")
	runImagesOver(t, st, addresses)
	asked := site.requestCount()
	if asked != 3 {
		t.Fatalf("first run made %d requests, want 3", asked)
	}
	runImagesOver(t, st, addresses)
	if site.requestCount() != asked {
		t.Errorf("the second run made %d more requests, want none", site.requestCount()-asked)
	}
}

func TestImagesOnAnotherHostAreFetchedAndNoRequestCarriesACookie(t *testing.T) {
	first := newCookieWitness(t, serveImages)
	second := newCookieWitness(t, serveImages)
	st, _ := openFreshStore(t)
	addresses := []string{first.URL + "/one.png", second.URL + "/two"}

	// A cookie handed to an images run would be the only way to put one on a request, so
	// the run is given one on purpose and has to keep it off the wire or refuse to start.
	cfg := fetch.RunConfig{
		Store: st, Images: true, Addresses: addresses, Rate: 1000, Timeout: 5 * time.Second,
		Cookie: siteCookie, CookieHost: first.hostKey(),
	}
	if _, err := fetch.Run(context.Background(), cfg); err == nil {
		t.Error("a run of images accepted a cookie")
	}
	cfg.Cookie, cfg.CookieHost = "", ""
	if summary, err := fetch.Run(context.Background(), cfg); err != nil || summary.Held != 2 {
		t.Fatalf("summary = %v, err %v, want both held", summary, err)
	}
	wantNoRequestSigned(t, "the first host", first.seen(), 1)
	wantNoRequestSigned(t, "the second host", second.seen(), 1)
}
