// Package discover produces the list of addresses a recipe describes: from the site's
// sitemap, from the links of one page, or from a command the site supplies.
//
// A discovery either gives the whole list or fails. A partial list looks exactly like a
// complete one to everything that reads it afterwards.
package discover

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/gabrielassisxyz/hoard/internal/hostkey"
	"github.com/gabrielassisxyz/hoard/internal/recipe"
)

// Result is what a discovery found.
type Result struct {
	// Addresses are the site's own, each once, in the order they were found.
	Addresses []string
	// OffSite counts addresses left out for being on another host than the recipe's.
	OffSite int
}

// Run finds the addresses of the recipe's site. It writes nothing: what to do with the
// list is the caller's decision.
func Run(ctx context.Context, r recipe.Recipe) (Result, error) {
	candidates, err := candidatesOf(ctx, r)
	if err != nil {
		return Result{}, err
	}
	result, err := keepTheSitesOwn(candidates, r)
	if err != nil {
		return Result{}, err
	}
	if len(result.Addresses) == 0 {
		return Result{}, fmt.Errorf("nothing found for %s; a site with no pages is a recipe that no longer matches it", r.Site.Name)
	}
	return result, nil
}

func candidatesOf(ctx context.Context, r recipe.Recipe) ([]string, error) {
	if r.Discover.Kind == recipe.KindCommand {
		return addressesFromCommand(ctx, r)
	}
	client, err := newSiteClient(r)
	if err != nil {
		return nil, err
	}
	defer client.close()
	switch r.Discover.Kind {
	case recipe.KindSitemap:
		return addressesFromSitemap(ctx, client, r)
	case recipe.KindLinks:
		return addressesFromLinks(ctx, client, r.Discover.URL)
	}
	return nil, fmt.Errorf("unknown kind %q", r.Discover.Kind)
}

// keepTheSitesOwn applies, in order: one row per address, the recipe's pattern, and the
// rule that a recipe collects one site. The pattern goes first so that the off-site count
// only reports addresses the recipe otherwise asked for.
func keepTheSitesOwn(candidates []string, r recipe.Recipe) (Result, error) {
	var result Result
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		address := strings.TrimSpace(candidate)
		if address == "" {
			continue
		}
		if _, duplicate := seen[address]; duplicate {
			continue
		}
		seen[address] = struct{}{}
		if r.Discover.Pattern != nil && !r.Discover.Pattern.MatchString(address) {
			continue
		}
		parsed, err := url.Parse(address)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			return Result{}, fmt.Errorf("%q is not an absolute http or https address", address)
		}
		if hostkey.Of(parsed) != r.Site.Host {
			result.OffSite++
			continue
		}
		result.Addresses = append(result.Addresses, address)
	}
	return result, nil
}
