package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabrielassisxyz/hoard/internal/store"
)

const showBase = `<base href="https://site.test/post/">`

func TestShowInsertsOneBaseRightAfterTheOpeningHead(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"plain head":        {"<html><head><title>x</title></head>", "<html><head>" + showBase + "<title>x</title></head>"},
		"head with a class": {`<head class="a>b">t`, `<head class="a>b">` + showBase + "t"},
		"upper case":        {"<HEAD>t", "<HEAD>" + showBase + "t"},
		"head then newline": {"<head\n>t", "<head\n>" + showBase + "t"},
		"self closing":      {"<head/>t", "<head/>" + showBase + "t"},
		"not header":        {"<header>h</header><head>t", "<header>h</header><head>" + showBase + "t"},
		"in a comment":      {"<!-- <head> --><head>t", "<!-- <head> --><head>" + showBase + "t"},
		"in a script":       {`<script>"<head>"</script><head>t`, `<script>"<head>"</script><head>` + showBase + "t"},
		"second head":       {"<head>a<head>b", "<head>" + showBase + "a<head>b"},
		"page has a base":   {`<head><base href="/x">`, "<head>" + showBase + `<base href="/x">`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := string(makeViewable([]byte(c.in), "https://site.test/post/")); got != c.want {
				t.Errorf("\ngot  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestShowEscapesTheAddressInTheBaseElement(t *testing.T) {
	got := string(makeViewable([]byte("<head>"), `https://site.test/a?x=1&y="2"`))
	want := `<head><base href="https://site.test/a?x=1&amp;y=&#34;2&#34;">`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestShowRemovesEveryIntegrityAttributeSpelling(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"double quoted": {`<link rel="stylesheet" integrity="sha384-a b" href="/c.css">`, `<link rel="stylesheet" href="/c.css">`},
		"single quoted": {`<link integrity='sha384-a' href=/c.css>`, `<link href=/c.css>`},
		"unquoted":      {`<link integrity=sha384-a href=/c.css>`, `<link href=/c.css>`},
		"bare":          {`<script integrity src=a.js></script>`, `<script src=a.js></script>`},
		"last, spaced":  {`<link href=a integrity = "x" >`, `<link href=a >`},
		"upper case":    {`<link INTEGRITY="x" href=a>`, `<link href=a>`},
		"tab before":    {"<link href=a\tintegrity=x>", "<link href=a>"},
		"two of them":   {`<a integrity=1 b=2 integrity="3">`, `<a b=2>`},
		"any element":   {`<img integrity="x" src=a>`, `<img src=a>`},
		"gt in a value": {`<a title="a>b" integrity="x">`, `<a title="a>b">`},
		"slash before":  {`<a href="a"/integrity=x>`, `<a href="a"/>`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := string(makeViewable([]byte(c.in), "https://site.test/post/"))
			if want := showBase + c.want; got != want {
				t.Errorf("\ngot  %q\nwant %q", got, want)
			}
		})
	}
}

func TestShowLeavesIntegrityAloneWhereItIsNotAnAttribute(t *testing.T) {
	kept := []string{
		`<p>the integrity of a page</p>`,
		`<!-- <link integrity="x"> -->`,
		`<script>var s = '<link integrity="x">';</script>`,
		`<SCRIPT type=x>integrity="y"</script >`,
		`<style>a[integrity="x"] { color: red }</style>`,
		`<link data-integrity="x" href=a>`,
		`<link x-integrity=x integrity-hash=y>`,
		`<a title="integrity=x" href=a>`,
		`<a title='a integrity="x"'>`,
		`<a href=integrity>`,
		`</a integrity=x>`,
		`<a integrity="never closed`,
	}
	for _, in := range kept {
		got := string(makeViewable([]byte(in), "https://site.test/post/"))
		if want := showBase + in; got != want {
			t.Errorf("\ngot  %q\nwant %q", got, want)
		}
	}
}

func TestShowRemovesIntegrityAfterAScriptThatMentionsIt(t *testing.T) {
	in := `<script>"</scripty>integrity=x"</script><link integrity=z href=a>`
	want := showBase + `<script>"</scripty>integrity=x"</script><link href=a>`
	if got := string(makeViewable([]byte(in), "https://site.test/post/")); got != want {
		t.Errorf("\ngot  %q\nwant %q", got, want)
	}
}

// A page with no head tag gets the element at its start, but never before a byte order mark
// or the doctype: either would change how the browser reads the page.
func TestShowWithoutHeadStartsWithBase(t *testing.T) {
	cases := map[string]struct{ before, after string }{
		"no doctype":          {"", "<p>no head here</p>"},
		"doctype":             {"<!doctype html>", "<p>no head here</p>"},
		"upper-case doctype":  {"\n<!DOCTYPE html>", "\n<p>no head here</p>"},
		"byte order mark":     {"\xef\xbb\xbf", "<p>no head here</p>"},
		"mark and doctype":    {"\xef\xbb\xbf<!doctype html>", "<p>no head here</p>"},
		"doctype never ended": {"", "<!doctype html"},
		"empty body":          {"", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := string(makeViewable([]byte(c.before+c.after), "https://site.test/post/"))
			if want := c.before + showBase + c.after; got != want {
				t.Errorf("\ngot  %q\nwant %q", got, want)
			}
		})
	}
}

// The body is not UTF-8, and the output has to be the input plus the element, minus the
// attribute, byte for byte.
func TestShowDoesNotReencodeAWindows1252Body(t *testing.T) {
	in := []byte("<html><head><meta charset=windows-1252><title>caf\xe9</title>" +
		"<link integrity=\"sha384-x\" href=a></head><body>\x93quoted\x94 \xa0 \xff</body></html>")
	want := []byte("<html><head>" + showBase + "<meta charset=windows-1252><title>caf\xe9</title>" +
		"<link href=a></head><body>\x93quoted\x94 \xa0 \xff</body></html>")
	if got := makeViewable(in, "https://site.test/post/"); !bytes.Equal(got, want) {
		t.Errorf("\ngot  %q\nwant %q", got, want)
	}
}

// showFixture is a store filled by `hoard fetch --list` from a site that redirects /old.
type showFixture struct {
	dir, siteURL string
}

const showPageBody = "<!doctype html><html><head><title>caf\xe9</title>" +
	"<link rel=stylesheet integrity=\"sha384-x\" href=\"/css/main.css\"></head><body>hi</body></html>"

func newShowFixture(t *testing.T) showFixture {
	t.Helper()
	site := newCLISite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/new/", http.StatusMovedPermanently)
		case "/nohead":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<p>bare</p>"))
		default:
			w.Header().Set("Content-Type", "text/html; charset=windows-1252")
			w.Write([]byte(showPageBody))
		}
	})
	dir := filepath.Join(t.TempDir(), "store")
	list := writeListFile(t, site.URL+"/page", site.URL+"/old", site.URL+"/nohead")
	if result := runHoardBinary(t, "fetch", "--store", dir, "--list", list, "--rate", "1000"); result.exitCode != 0 {
		t.Fatalf("fetch: exit %d: %s", result.exitCode, result.stderr)
	}
	seedListStore(t, dir, []listSeed{
		{address: "https://gone.test/failed", state: store.StateFailed},
		{address: "https://gone.test/pending", state: store.StatePending},
	})
	return showFixture{dir: dir, siteURL: site.URL}
}

func TestShowWritesTheStoredBodyWithABaseAndNoIntegrity(t *testing.T) {
	fx := newShowFixture(t)

	result := runHoardBinary(t, "show", "--store", fx.dir, "--address", fx.siteURL+"/page")

	want := "<!doctype html><html><head>" + `<base href="` + fx.siteURL + `/page">` + "<title>caf\xe9</title>" +
		"<link rel=stylesheet href=\"/css/main.css\"></head><body>hi</body></html>"
	if result.exitCode != 0 || result.stdout != want || result.stderr != "" {
		t.Errorf("exit %d, stderr %q\ngot  %q\nwant %q", result.exitCode, result.stderr, result.stdout, want)
	}
}

func TestShowNamesWhereARedirectedAnswerCameFrom(t *testing.T) {
	fx := newShowFixture(t)

	result := runHoardBinary(t, "show", "--store", fx.dir, "--address", fx.siteURL+"/old")

	want := `<head><base href="` + fx.siteURL + `/new/">`
	if result.exitCode != 0 || !strings.Contains(result.stdout, want) {
		t.Errorf("exit %d, want %q in %q", result.exitCode, want, result.stdout)
	}
	if strings.Contains(result.stdout, "/old") {
		t.Errorf("the address as requested is in the output: %q", result.stdout)
	}
}

func TestShowPutsTheBaseFirstWhenThePageHasNoHead(t *testing.T) {
	fx := newShowFixture(t)

	result := runHoardBinary(t, "show", "--store", fx.dir, "--address", fx.siteURL+"/nohead")

	if want := `<base href="` + fx.siteURL + `/nohead"><p>bare</p>`; result.stdout != want {
		t.Errorf("got %q, want %q", result.stdout, want)
	}
}

func TestShowOutWritesTheFileAndPrintsNothing(t *testing.T) {
	fx := newShowFixture(t)
	out := filepath.Join(t.TempDir(), "copy.html")

	result := runHoardBinary(t, "show", "--store", fx.dir, "--address", fx.siteURL+"/nohead", "--out", out)
	written, err := os.ReadFile(out)

	if result.exitCode != 0 || result.stdout != "" || result.stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", result.exitCode, result.stdout, result.stderr)
	}
	if want := `<base href="` + fx.siteURL + `/nohead"><p>bare</p>`; err != nil || string(written) != want {
		t.Errorf("file holds %q (%v), want %q", written, err, want)
	}
}

func TestShowExitsOneForAnAddressWithNoBodyToShow(t *testing.T) {
	fx := newShowFixture(t)
	cases := map[string]struct{ address, reason string }{
		"not held": {"https://nowhere.test/x", "does not hold"},
		"pending":  {"https://gone.test/pending", "is pending"},
		"failed":   {"https://gone.test/failed", "is failed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			result := runHoardBinary(t, "show", "--store", fx.dir, "--address", c.address)
			if result.exitCode != 1 || result.stdout != "" || !strings.Contains(result.stderr, "hoard show:") || !strings.Contains(result.stderr, c.reason) {
				t.Errorf("exit %d, stdout %q, stderr %q", result.exitCode, result.stdout, result.stderr)
			}
		})
	}
}

func TestShowExitsOneAndCreatesNothingWhenThereIsNoStore(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "nested", "typo")
	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	before := listTestTree(t, root)

	for _, dir := range []string{missing, empty} {
		result := runHoardBinary(t, "show", "--store", dir, "--address", "https://site.test/")
		if result.exitCode != 1 || result.stdout != "" || !strings.Contains(result.stderr, dir) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", dir, result.exitCode, result.stdout, result.stderr)
		}
	}
	if after := listTestTree(t, root); !slices.Equal(before, after) {
		t.Errorf("the tree changed:\nbefore %v\nafter  %v", before, after)
	}
}

func TestShowExitsTwoWhenTheStoreOrTheAddressIsMissing(t *testing.T) {
	cases := map[string][]string{
		"no flags":      {"show"},
		"no store":      {"show", "--address", "https://site.test/"},
		"no address":    {"show", "--store", "somewhere"},
		"empty address": {"show", "--store", "somewhere", "--address", ""},
		"stray":         {"show", "--store", "somewhere", "--address", "a", "extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			result := runHoardBinary(t, args...)
			if result.exitCode != 2 || result.stdout != "" || !strings.Contains(result.stderr, "usage: hoard show") {
				t.Errorf("exit %d, stdout %q, stderr %q", result.exitCode, result.stdout, result.stderr)
			}
		})
	}
}

func TestShowLeavesEveryFileOfTheStoreAsItWas(t *testing.T) {
	fx := newShowFixture(t)
	before := listTestTree(t, fx.dir)

	for _, path := range []string{"/page", "/old", "/nohead", "/absent"} {
		runHoardBinary(t, "show", "--store", fx.dir, "--address", fx.siteURL+path)
	}

	if after := listTestTree(t, fx.dir); !slices.Equal(before, after) {
		t.Errorf("show changed the store \nbefore %v\nafter  %v", before, after)
	}
}
