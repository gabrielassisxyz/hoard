package discover

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/gabrielassisxyz/hoard/internal/hostkey"
	"github.com/gabrielassisxyz/hoard/internal/recipe"
)

// sitemapDocument is either kind of sitemap file; which one it is, is said by its root.
type sitemapDocument struct {
	XMLName  xml.Name
	Pages    []sitemapEntry `xml:"url"`
	Children []sitemapEntry `xml:"sitemap"`
}

type sitemapEntry struct {
	Loc string `xml:"loc"`
}

const (
	sitemapRootOfPages    = "urlset"
	sitemapRootOfSitemaps = "sitemapindex"
)

// addressesFromSitemap reads the recipe's sitemap. An index is followed exactly one level:
// refusing to read one made a run over a 2,592-page site end with a single page, and
// following them without a bound has no end a recipe could state.
func addressesFromSitemap(ctx context.Context, client *siteClient, r recipe.Recipe) ([]string, error) {
	top, err := fetchSitemap(ctx, client, r.Discover.URL)
	if err != nil {
		return nil, err
	}
	if top.XMLName.Local == sitemapRootOfPages {
		return locationsOf(top.Pages), nil
	}
	var addresses []string
	for _, location := range locationsOf(top.Children) {
		child, err := childSitemapAddress(location, r.Site.Host)
		if err != nil {
			return nil, err
		}
		document, err := fetchSitemap(ctx, client, child)
		if err != nil {
			return nil, err
		}
		if document.XMLName.Local != sitemapRootOfPages {
			return nil, fmt.Errorf("%s is an index inside an index; a sitemap index is read one level deep", location)
		}
		addresses = append(addresses, locationsOf(document.Pages)...)
	}
	return addresses, nil
}

func childSitemapAddress(location, siteHost string) (*url.URL, error) {
	parsed, err := url.Parse(location)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, fmt.Errorf("the sitemap index names %q, which is not an absolute http or https address", location)
	}
	if hostkey.Of(parsed) != siteHost {
		return nil, fmt.Errorf("the sitemap index names %s, which is on %q and not on the site %q", location, hostkey.Of(parsed), siteHost)
	}
	return parsed, nil
}

func fetchSitemap(ctx context.Context, client *siteClient, address *url.URL) (sitemapDocument, error) {
	body, _, err := client.get(ctx, address)
	if err != nil {
		return sitemapDocument{}, err
	}
	document, err := parseSitemap(body)
	if err != nil {
		return sitemapDocument{}, fmt.Errorf("%s: %w", address, err)
	}
	return document, nil
}

var gzipMagic = []byte{0x1f, 0x8b}

// parseSitemap reads a sitemap file. A "sitemap.xml.gz" is served as an opaque file, not
// as a compressed transfer, so the transport leaves it compressed and it is opened here.
func parseSitemap(body []byte) (sitemapDocument, error) {
	if bytes.HasPrefix(body, gzipMagic) {
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return sitemapDocument{}, fmt.Errorf("not a readable gzip file: %w", err)
		}
		opened, err := io.ReadAll(io.LimitReader(reader, maxDocumentBytes+1))
		if err != nil {
			return sitemapDocument{}, fmt.Errorf("not a readable gzip file: %w", err)
		}
		if len(opened) > maxDocumentBytes {
			return sitemapDocument{}, fmt.Errorf("sitemap is larger than %d bytes once decompressed", maxDocumentBytes)
		}
		body = opened
	}
	var document sitemapDocument
	if err := xml.Unmarshal(body, &document); err != nil {
		return sitemapDocument{}, fmt.Errorf("not a sitemap: %w", err)
	}
	if root := document.XMLName.Local; root != sitemapRootOfPages && root != sitemapRootOfSitemaps {
		return sitemapDocument{}, fmt.Errorf("not a sitemap: its root is <%s>, not <%s> or <%s>", root, sitemapRootOfPages, sitemapRootOfSitemaps)
	}
	return document, nil
}

func locationsOf(entries []sitemapEntry) []string {
	locations := make([]string, 0, len(entries))
	for _, entry := range entries {
		if location := strings.TrimSpace(entry.Loc); location != "" {
			locations = append(locations, location)
		}
	}
	return locations
}
