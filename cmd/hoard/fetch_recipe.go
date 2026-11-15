package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/recipe"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

// runFetchByRecipe fetches what was discovered for a site, at the pace its recipe states
// and signed in when the recipe names a cookie. The run answers for every address the
// ledger holds, so its summary and its exit code describe the whole capture.
func runFetchByRecipe(ctx context.Context, opts fetchOptions, stdout, stderr io.Writer) int {
	site, err := recipe.Load(opts.recipePath)
	if err != nil {
		// A recipe that cannot be read is a command that cannot be read: nothing was tried.
		fmt.Fprintf(stderr, "hoard fetch: %v\n", err)
		return exitCodeUsage
	}
	// Read before the store is opened or anything is asked of the site: a run that was
	// meant to be signed in must not start signed out.
	cookie, err := site.CookieHeader()
	if err != nil {
		return reportRunFailure(stderr, err)
	}
	st, err := store.Open(opts.storeDir)
	if err != nil {
		return reportRunFailure(stderr, err)
	}
	defer st.Close()
	addresses, err := everyLedgerAddress(st)
	if err != nil {
		return reportRunFailure(stderr, err)
	}

	summary, err := fetch.Run(ctx, fetch.RunConfig{
		Store:      st,
		Addresses:  addresses,
		Workers:    site.Fetch.Workers,
		Rate:       site.Fetch.Rate,
		Timeout:    site.Fetch.Timeout,
		UserAgent:  site.Fetch.UserAgent,
		Cookie:     cookie,
		CookieHost: site.Site.Host,
	})
	if err != nil {
		return reportRunFailure(stderr, err)
	}
	fmt.Fprintln(stdout, summary)
	if !summary.Complete() {
		return exitCodeIncomplete
	}
	return exitCodeComplete
}

func everyLedgerAddress(st *store.Store) ([]string, error) {
	pages, err := st.Pages()
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, errors.New("the store holds no address yet: run hoard discover with this recipe first")
	}
	addresses := make([]string, len(pages))
	for i, page := range pages {
		addresses[i] = page.Address
	}
	return addresses, nil
}
