package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRecipe(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recipe.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustLoad(t *testing.T, content string) Recipe {
	t.Helper()
	recipe, err := Load(writeRecipe(t, content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return recipe
}

// loadProblems returns the text of the error Load gives for content, and fails the test
// when Load accepts it.
func loadProblems(t *testing.T, content string) string {
	t.Helper()
	recipe, err := Load(writeRecipe(t, content))
	if err == nil {
		t.Fatalf("Load accepted the recipe as %+v, want an error", recipe)
	}
	return err.Error()
}

const everySitemapKey = `
[site]
name = "example"
host = "Example.com"

[discover]
kind = "sitemap"
url = "https://example.com/sitemap.xml"
pattern = "^https://example\\.com/p/"

[fetch]
workers = 3
rate = 1.5
timeout = "45s"
user_agent = "hoard-test"
cookie_file = "/secrets/example.cookie"

[verify]
min_pages = 200
min_body_bytes = 2000
forbidden = ["Subscribe to read", "paywall-box"]
`

func TestLoadReadsEveryKeyOfASitemapRecipe(t *testing.T) {
	path := writeRecipe(t, everySitemapKey)
	recipe, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if recipe.Site.Name != "example" || recipe.Site.Host != "example.com" {
		t.Errorf("site = %+v, want name example and host example.com", recipe.Site)
	}
	if recipe.Dir != filepath.Dir(path) {
		t.Errorf("Dir = %q, want %q", recipe.Dir, filepath.Dir(path))
	}
	discover := recipe.Discover
	if discover.Kind != KindSitemap || discover.URL == nil || discover.URL.String() != "https://example.com/sitemap.xml" {
		t.Errorf("discover = %+v, want the sitemap at https://example.com/sitemap.xml", discover)
	}
	if discover.Pattern == nil || !discover.Pattern.MatchString("https://example.com/p/one") || discover.Pattern.MatchString("https://example.com/about") {
		t.Errorf("pattern %v does not select /p/ addresses only", discover.Pattern)
	}
	if discover.Command != nil {
		t.Errorf("command = %v, want none for a sitemap", discover.Command)
	}
	wantFetch := Fetch{Workers: 3, Rate: 1.5, Timeout: 45 * time.Second, UserAgent: "hoard-test", CookieFile: "/secrets/example.cookie"}
	if recipe.Fetch != wantFetch {
		t.Errorf("fetch = %+v, want %+v", recipe.Fetch, wantFetch)
	}
	verify := recipe.Verify
	if verify.MinPages != 200 || verify.MinBodyBytes != 2000 || strings.Join(verify.Forbidden, "|") != "Subscribe to read|paywall-box" {
		t.Errorf("verify = %+v", verify)
	}
}

func TestLoadReadsALinksRecipe(t *testing.T) {
	recipe := mustLoad(t, `
[site]
name = "essays"
host = "127.0.0.1:4000"

[discover]
kind = "links"
url = "http://127.0.0.1:4000/articles.html"
pattern = "\\.html$"
`)
	if recipe.Discover.Kind != KindLinks || recipe.Discover.URL.Path != "/articles.html" || recipe.Discover.Pattern == nil {
		t.Errorf("discover = %+v", recipe.Discover)
	}
	if recipe.Site.Host != "127.0.0.1:4000" {
		t.Errorf("host = %q, want the port kept", recipe.Site.Host)
	}
}

func TestLoadReadsACommandRecipe(t *testing.T) {
	recipe := mustLoad(t, `
[site]
name = "data"
host = "example.com"

[discover]
kind = "command"
command = ["./list-pages", "--all"]
`)
	discover := recipe.Discover
	if discover.Kind != KindCommand || strings.Join(discover.Command, " ") != "./list-pages --all" {
		t.Errorf("discover = %+v", discover)
	}
	if discover.URL != nil || discover.Pattern != nil {
		t.Errorf("discover = %+v, want no url and no pattern for a command", discover)
	}
}

func TestLoadDefaultsWhatTheRecipeDoesNotState(t *testing.T) {
	recipe := mustLoad(t, `
[site]
name = "example"
host = "example.com"

[discover]
kind = "sitemap"
url = "https://example.com/sitemap.xml"
`)
	if recipe.Verify.MinPages != 1 || recipe.Verify.MinBodyBytes != 0 || len(recipe.Verify.Forbidden) != 0 {
		t.Errorf("verify = %+v, want at least 1 page and nothing else", recipe.Verify)
	}
	if recipe.Fetch != (Fetch{}) {
		t.Errorf("fetch = %+v, want every value left for the fetcher's default", recipe.Fetch)
	}
	if recipe.Discover.Pattern != nil {
		t.Errorf("pattern = %v, want none", recipe.Discover.Pattern)
	}
}

func TestLoadAcceptsAWholeNumberRate(t *testing.T) {
	recipe := mustLoad(t, strings.Replace(everySitemapKey, "rate = 1.5", "rate = 2", 1))
	if recipe.Fetch.Rate != 2 {
		t.Errorf("rate = %v, want 2", recipe.Fetch.Rate)
	}
}

func TestLoadRefusesAKeyItDoesNotKnow(t *testing.T) {
	cases := []struct{ name, addition, key string }{
		{"top level", "colour = \"red\"\n" + everySitemapKey, "colour"},
		{"in site", strings.Replace(everySitemapKey, "[discover]", "nmae = \"x\"\n\n[discover]", 1), "site.nmae"},
		{"in discover", strings.Replace(everySitemapKey, "[fetch]", "limit = 5\n\n[fetch]", 1), "discover.limit"},
		{"in fetch", strings.Replace(everySitemapKey, "[verify]", "retries = 5\n\n[verify]", 1), "fetch.retries"},
		{"in verify", everySitemapKey + "max_pages = 9\n", "verify.max_pages"},
		{"a whole table", everySitemapKey + "\n[assets]\nkeep = true\n", "assets"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text := loadProblems(t, c.addition)
			if !strings.Contains(text, c.key+": unknown key") && !strings.Contains(text, c.key+".keep: unknown key") {
				t.Errorf("error does not name %q as unknown:\n%s", c.key, text)
			}
		})
	}
}

func TestLoadRefusesAKeyOfAnotherKind(t *testing.T) {
	cases := []struct{ name, content, key string }{
		{"command under sitemap", strings.Replace(everySitemapKey, "[fetch]", "command = [\"./x\"]\n\n[fetch]", 1), "discover.command"},
		{"url under command", "[site]\nname = \"a\"\nhost = \"example.com\"\n[discover]\nkind = \"command\"\ncommand = [\"./x\"]\nurl = \"https://example.com/\"\n", "discover.url"},
		{"pattern under command", "[site]\nname = \"a\"\nhost = \"example.com\"\n[discover]\nkind = \"command\"\ncommand = [\"./x\"]\npattern = \"x\"\n", "discover.pattern"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if text := loadProblems(t, c.content); !strings.Contains(text, c.key+": does not belong to kind") {
				t.Errorf("error does not refuse %q for its kind:\n%s", c.key, text)
			}
		})
	}
}

func TestLoadNamesTheKeyOfEachRefusedValue(t *testing.T) {
	replace := func(old, new string) string { return strings.Replace(everySitemapKey, old, new, 1) }
	linksRecipe := "[site]\nname = \"a\"\nhost = \"example.com\"\n[discover]\nkind = \"links\"\nurl = \"https://example.com/i\"\n"
	commandRecipe := "[site]\nname = \"a\"\nhost = \"example.com\"\n[discover]\nkind = \"command\"\n"
	cases := []struct{ name, content, key string }{
		{"no site table", "[discover]\nkind = \"sitemap\"\nurl = \"https://example.com/s.xml\"\n", "site: "},
		{"no discover table", "[site]\nname = \"a\"\nhost = \"example.com\"\n", "discover: "},
		{"no name", replace("name = \"example\"\n", ""), "site.name: "},
		{"empty name", replace("name = \"example\"", "name = \"\""), "site.name: "},
		{"name with a space", replace("name = \"example\"", "name = \"my site\""), "site.name: "},
		{"no host", replace("host = \"Example.com\"\n", ""), "site.host: "},
		{"host with a scheme", replace("host = \"Example.com\"", "host = \"https://example.com\""), "site.host: "},
		{"no kind", replace("kind = \"sitemap\"\n", ""), "discover.kind: "},
		{"unknown kind", replace("kind = \"sitemap\"", "kind = \"crawl\""), "discover.kind: "},
		{"no url", replace("url = \"https://example.com/sitemap.xml\"\n", ""), "discover.url: "},
		{"relative url", replace("url = \"https://example.com/sitemap.xml\"", "url = \"/sitemap.xml\""), "discover.url: "},
		{"ftp url", replace("url = \"https://example.com/sitemap.xml\"", "url = \"ftp://example.com/s.xml\""), "discover.url: "},
		{"url on another host", replace("url = \"https://example.com/sitemap.xml\"", "url = \"https://other.example/sitemap.xml\""), "discover.url: "},
		{"url on another port", replace("url = \"https://example.com/sitemap.xml\"", "url = \"https://example.com:8443/sitemap.xml\""), "discover.url: "},
		{"links without a pattern", linksRecipe, "discover.pattern: "},
		{"pattern that does not compile", replace("pattern = \"^https://example\\\\.com/p/\"", "pattern = \"(\""), "discover.pattern: "},
		{"command kind without a command", commandRecipe, "discover.command: "},
		{"command kind with an empty command", commandRecipe + "command = []\n", "discover.command: "},
		{"workers of zero", replace("workers = 3", "workers = 0"), "fetch.workers: "},
		{"rate of zero", replace("rate = 1.5", "rate = 0.0"), "fetch.rate: "},
		{"negative rate", replace("rate = 1.5", "rate = -1.0"), "fetch.rate: "},
		{"timeout that is not a duration", replace("timeout = \"45s\"", "timeout = \"soon\""), "fetch.timeout: "},
		{"timeout of zero", replace("timeout = \"45s\"", "timeout = \"0s\""), "fetch.timeout: "},
		{"empty user agent", replace("user_agent = \"hoard-test\"", "user_agent = \"\""), "fetch.user_agent: "},
		{"empty cookie file", replace("cookie_file = \"/secrets/example.cookie\"", "cookie_file = \"\""), "fetch.cookie_file: "},
		{"negative min_pages", replace("min_pages = 200", "min_pages = -1"), "verify.min_pages: "},
		{"negative min_body_bytes", replace("min_body_bytes = 2000", "min_body_bytes = -1"), "verify.min_body_bytes: "},
		{"empty forbidden entry", replace("forbidden = [\"Subscribe to read\", \"paywall-box\"]", "forbidden = [\"x\", \"\"]"), "verify.forbidden: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if text := loadProblems(t, c.content); !strings.Contains(text, "- "+c.key) {
				t.Errorf("error does not name %q:\n%s", strings.TrimSuffix(c.key, ": "), text)
			}
		})
	}
}

func TestLoadNamesAValueOfTheWrongType(t *testing.T) {
	text := loadProblems(t, strings.Replace(everySitemapKey, "workers = 3", "workers = \"three\"", 1))
	if !strings.Contains(text, "workers") {
		t.Errorf("error does not name the key whose value has the wrong type:\n%s", text)
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	content := strings.NewReplacer(
		"rate = 1.5", "rate = 0.0",
		"host = \"Example.com\"\n", "",
		"min_pages = 200", "min_pages = 200\nmax_pages = 9",
	).Replace(everySitemapKey)
	text := loadProblems(t, content)
	for _, key := range []string{"fetch.rate: ", "site.host: ", "verify.max_pages: unknown key"} {
		if !strings.Contains(text, key) {
			t.Errorf("error leaves out %q:\n%s", key, text)
		}
	}
}

func TestLoadSaysWhenTheFileCannotBeRead(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.toml")); err == nil {
		t.Error("Load of a missing file returned no error")
	}
	if text := loadProblems(t, "[site\nname = "); !strings.Contains(text, "line") {
		t.Errorf("a syntax error does not say where it is:\n%s", text)
	}
}

func TestLoadResolvesTheCookieFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	withCookie := func(written string) Recipe {
		return mustLoad(t, strings.Replace(everySitemapKey, "/secrets/example.cookie", written, 1))
	}
	if got, want := withCookie("~/secrets/site.cookie").Fetch.CookieFile, filepath.Join(home, "secrets", "site.cookie"); got != want {
		t.Errorf("a path under ~/ resolved to %q, want %q", got, want)
	}
	relative := withCookie("site.cookie")
	if want := filepath.Join(relative.Dir, "site.cookie"); relative.Fetch.CookieFile != want {
		t.Errorf("a relative path resolved to %q, want %q beside the recipe", relative.Fetch.CookieFile, want)
	}
	if got := withCookie("/var/secrets/site.cookie").Fetch.CookieFile; got != "/var/secrets/site.cookie" {
		t.Errorf("an absolute path became %q", got)
	}
}
