package discover

import (
	"bytes"
	"context"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// addressesFromLinks returns every link of the one page at address, as an absolute address
// without its fragment. Which of them are pages is the recipe's pattern's decision.
func addressesFromLinks(ctx context.Context, client *siteClient, address *url.URL) ([]string, error) {
	body, servedFrom, err := client.get(ctx, address)
	if err != nil {
		return nil, err
	}
	return linksOf(body, servedFrom), nil
}

// linksOf resolves each <a href> the way a browser would: against the page's <base href>
// when it has one, and otherwise against the address the page was finally served from,
// which after a redirect is not the address that was asked for.
func linksOf(page []byte, servedFrom *url.URL) []string {
	hrefs, baseHref := anchorsAndBase(page)
	base := servedFrom
	if baseHref != "" {
		if declared, err := servedFrom.Parse(baseHref); err == nil {
			base = declared
		}
	}
	var links []string
	for _, href := range hrefs {
		target, err := base.Parse(strings.TrimSpace(href))
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") {
			continue
		}
		// Two links that differ only by fragment are one page.
		target.Fragment = ""
		target.RawFragment = ""
		links = append(links, target.String())
	}
	return links
}

// anchorsAndBase walks the page once. Only the first <base href> counts, as in a browser.
func anchorsAndBase(page []byte) (hrefs []string, baseHref string) {
	tokens := html.NewTokenizer(bytes.NewReader(page))
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return hrefs, baseHref
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokens.Token()
			href, hasHref := attribute(token, "href")
			if !hasHref {
				continue
			}
			switch token.Data {
			case "a":
				hrefs = append(hrefs, href)
			case "base":
				if baseHref == "" {
					baseHref = href
				}
			}
		}
	}
}

func attribute(token html.Token, name string) (string, bool) {
	for _, attr := range token.Attr {
		if attr.Key == name {
			return attr.Val, true
		}
	}
	return "", false
}
