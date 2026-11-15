// Package recipe reads the file that describes one site: where its list of addresses comes
// from, how it may be asked, and what a complete capture of it looks like.
//
// A recipe holds values only. A key the reader does not know is an error, because a
// misspelt setting that is silently ignored leaves the site described by its defaults.
package recipe

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/gabrielassisxyz/hoard/internal/hostkey"
)

// The ways a site's list of addresses can be found.
const (
	KindSitemap = "sitemap"
	KindLinks   = "links"
	KindCommand = "command"
)

// Recipe is one site's description, checked.
type Recipe struct {
	Site     Site
	Discover Discover
	Fetch    Fetch
	Verify   Verify
	// Dir is the directory the recipe file is in. A relative path in the recipe, and the
	// discover command, are relative to it.
	Dir string
}

type Site struct {
	Name string
	// Host is the site's key as hostkey.Of gives it, so it can be compared with the key of
	// any address.
	Host string
}

type Discover struct {
	Kind string
	// URL is the sitemap, or the page whose links are the list. Nil for KindCommand.
	URL *url.URL
	// Pattern keeps only the addresses it matches. Nil when the recipe states none.
	Pattern *regexp.Regexp
	// Command is the program and its arguments. Nil unless Kind is KindCommand.
	Command []string
}

// Fetch is how the site may be asked. A zero value means the recipe does not say, and the
// fetcher's own default applies.
type Fetch struct {
	Workers   int
	Rate      float64
	Timeout   time.Duration
	UserAgent string
	// CookieFile is absolute, or empty when the site needs no login.
	CookieFile string
}

type Verify struct {
	MinPages     int
	MinBodyBytes int64
	Forbidden    []string
}

// A capture that holds nothing never verifies, even for a recipe that states no criterion.
const defaultMinPages = 1

var siteNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// The file as written. Every value is a pointer or a slice so that "absent" and "zero" stay
// different: a rate of 0 is a mistake, a missing rate is a default.
type recipeFile struct {
	Site     *siteTable     `toml:"site"`
	Discover *discoverTable `toml:"discover"`
	Fetch    *fetchTable    `toml:"fetch"`
	Verify   *verifyTable   `toml:"verify"`
}

type siteTable struct {
	Name *string `toml:"name"`
	Host *string `toml:"host"`
}

type discoverTable struct {
	Kind    *string  `toml:"kind"`
	URL     *string  `toml:"url"`
	Pattern *string  `toml:"pattern"`
	Command []string `toml:"command"`
}

type fetchTable struct {
	Workers    *int64   `toml:"workers"`
	Rate       *float64 `toml:"rate"`
	Timeout    *string  `toml:"timeout"`
	UserAgent  *string  `toml:"user_agent"`
	CookieFile *string  `toml:"cookie_file"`
}

type verifyTable struct {
	MinPages     *int64   `toml:"min_pages"`
	MinBodyBytes *int64   `toml:"min_body_bytes"`
	Forbidden    []string `toml:"forbidden"`
}

// Load reads and checks the recipe at path. Its error names every problem the file has, so
// a recipe is fixed in one pass instead of one run per mistake.
func Load(path string) (Recipe, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Recipe{}, fmt.Errorf("recipe %s: %w", path, err)
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return Recipe{}, fmt.Errorf("recipe %s: %w", path, err)
	}
	var file recipeFile
	if err := toml.Unmarshal(content, &file); err != nil {
		return Recipe{}, fmt.Errorf("recipe %s: %s", path, describeDecodeError(err))
	}
	found := &problems{}
	found.addAll(unknownKeys(content))
	recipe := Recipe{Dir: filepath.Dir(absolute)}
	recipe.Site = checkSite(file.Site, found)
	recipe.Discover = checkDiscover(file.Discover, recipe.Site.Host, found)
	recipe.Fetch = checkFetch(file.Fetch, recipe.Dir, found)
	recipe.Verify = checkVerify(file.Verify, found)
	if len(found.list) > 0 {
		return Recipe{}, fmt.Errorf("recipe %s:\n  - %s", path, strings.Join(found.list, "\n  - "))
	}
	return recipe, nil
}

type problems struct {
	list []string
}

func (p *problems) add(key, format string, args ...any) {
	p.list = append(p.list, key+": "+fmt.Sprintf(format, args...))
}

func (p *problems) addAll(more []string) {
	p.list = append(p.list, more...)
}

// unknownKeys decodes a second time, strictly, only to collect the keys nothing reads.
// Keeping it apart from the lenient pass means the other checks never depend on what a
// strict decode leaves in the value when it fails.
func unknownKeys(content []byte) []string {
	var discard recipeFile
	err := toml.NewDecoder(bytes.NewReader(content)).DisallowUnknownFields().Decode(&discard)
	var strict *toml.StrictMissingError
	if !errors.As(err, &strict) {
		return nil
	}
	var found []string
	for _, missing := range strict.Errors {
		row, _ := missing.Position()
		found = append(found, fmt.Sprintf("%s: unknown key (line %d)", strings.Join(missing.Key(), "."), row))
	}
	return found
}

func describeDecodeError(err error) string {
	var decode *toml.DecodeError
	if !errors.As(err, &decode) {
		return err.Error()
	}
	row, column := decode.Position()
	where := fmt.Sprintf("line %d, column %d", row, column)
	if key := strings.Join(decode.Key(), "."); key != "" {
		return fmt.Sprintf("%s: %s (%s)", key, decode.Error(), where)
	}
	return fmt.Sprintf("%s (%s)", decode.Error(), where)
}

func checkSite(table *siteTable, found *problems) Site {
	if table == nil {
		found.add("site", "the table is missing")
		return Site{}
	}
	var site Site
	switch {
	case table.Name == nil || *table.Name == "":
		found.add("site.name", "is missing")
	case !siteNamePattern.MatchString(*table.Name):
		found.add("site.name", "%q may hold only lower-case letters, digits, '.' and '-'", *table.Name)
	default:
		site.Name = *table.Name
	}
	if table.Host == nil || *table.Host == "" {
		found.add("site.host", "is missing")
		return site
	}
	host, err := hostkey.Normalize(*table.Host)
	if err != nil {
		found.add("site.host", "%v", err)
		return site
	}
	site.Host = host
	return site
}

func checkDiscover(table *discoverTable, siteHost string, found *problems) Discover {
	if table == nil {
		found.add("discover", "the table is missing")
		return Discover{}
	}
	if table.Kind == nil || *table.Kind == "" {
		found.add("discover.kind", "is missing: one of %q, %q, %q", KindSitemap, KindLinks, KindCommand)
		return Discover{}
	}
	discover := Discover{Kind: *table.Kind}
	switch discover.Kind {
	case KindSitemap, KindLinks:
		if table.Command != nil {
			found.add("discover.command", "does not belong to kind %q", discover.Kind)
		}
		discover.URL = checkDiscoverURL(table.URL, siteHost, found)
		discover.Pattern = checkPattern(table.Pattern, discover.Kind == KindLinks, found)
	case KindCommand:
		if table.URL != nil {
			found.add("discover.url", "does not belong to kind %q", KindCommand)
		}
		if table.Pattern != nil {
			found.add("discover.pattern", "does not belong to kind %q", KindCommand)
		}
		discover.Command = checkCommand(table.Command, found)
	default:
		found.add("discover.kind", "%q is not one of %q, %q, %q", discover.Kind, KindSitemap, KindLinks, KindCommand)
	}
	return discover
}

// The address must be on the recipe's own site: a recipe describes one site, and its
// session cookie is only ever sent there.
func checkDiscoverURL(written *string, siteHost string, found *problems) *url.URL {
	if written == nil || *written == "" {
		found.add("discover.url", "is missing")
		return nil
	}
	parsed, err := url.Parse(*written)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		found.add("discover.url", "%q is not an absolute http or https address", *written)
		return nil
	}
	if siteHost != "" && hostkey.Of(parsed) != siteHost {
		found.add("discover.url", "is on %q, not on site.host %q", hostkey.Of(parsed), siteHost)
		return nil
	}
	return parsed
}

func checkPattern(written *string, required bool, found *problems) *regexp.Regexp {
	if written == nil || *written == "" {
		if required {
			found.add("discover.pattern", "is missing: kind %q needs to know which links are pages", KindLinks)
		}
		return nil
	}
	pattern, err := regexp.Compile(*written)
	if err != nil {
		found.add("discover.pattern", "%v", err)
		return nil
	}
	return pattern
}

func checkCommand(written []string, found *problems) []string {
	if len(written) == 0 || written[0] == "" {
		found.add("discover.command", "is missing: the program to run, then its arguments")
		return nil
	}
	return written
}

func checkFetch(table *fetchTable, recipeDir string, found *problems) Fetch {
	if table == nil {
		return Fetch{}
	}
	var fetch Fetch
	if table.Workers != nil {
		if *table.Workers < 1 {
			found.add("fetch.workers", "must be at least 1")
		}
		fetch.Workers = int(*table.Workers)
	}
	if table.Rate != nil {
		if !(*table.Rate > 0) {
			found.add("fetch.rate", "must be above 0 requests per second")
		}
		fetch.Rate = *table.Rate
	}
	if table.Timeout != nil {
		timeout, err := time.ParseDuration(*table.Timeout)
		if err != nil || timeout <= 0 {
			found.add("fetch.timeout", "%q is not a duration above zero, such as \"30s\"", *table.Timeout)
		}
		fetch.Timeout = timeout
	}
	if table.UserAgent != nil {
		if *table.UserAgent == "" {
			found.add("fetch.user_agent", "cannot be empty")
		}
		fetch.UserAgent = *table.UserAgent
	}
	if table.CookieFile != nil {
		fetch.CookieFile = resolveCookiePath(*table.CookieFile, recipeDir, found)
	}
	return fetch
}

// A cookie never sits beside its recipe by accident: "~/" is the home directory, and only
// a path that is plainly relative is taken from the recipe's own directory.
func resolveCookiePath(written, recipeDir string, found *problems) string {
	if written == "" {
		found.add("fetch.cookie_file", "cannot be empty")
		return ""
	}
	if rest, underHome := strings.CutPrefix(written, "~/"); underHome {
		home, err := os.UserHomeDir()
		if err != nil {
			found.add("fetch.cookie_file", "%q needs a home directory: %v", written, err)
			return ""
		}
		return filepath.Join(home, rest)
	}
	if filepath.IsAbs(written) {
		return filepath.Clean(written)
	}
	return filepath.Join(recipeDir, written)
}

func checkVerify(table *verifyTable, found *problems) Verify {
	verify := Verify{MinPages: defaultMinPages}
	if table == nil {
		return verify
	}
	if table.MinPages != nil {
		if *table.MinPages < 0 {
			found.add("verify.min_pages", "cannot be negative")
		}
		verify.MinPages = int(*table.MinPages)
	}
	if table.MinBodyBytes != nil {
		if *table.MinBodyBytes < 0 {
			found.add("verify.min_body_bytes", "cannot be negative")
		}
		verify.MinBodyBytes = *table.MinBodyBytes
	}
	for position, entry := range table.Forbidden {
		if entry == "" {
			found.add("verify.forbidden", "entry %d is empty, and an empty string is in every page", position+1)
		}
	}
	verify.Forbidden = table.Forbidden
	return verify
}
