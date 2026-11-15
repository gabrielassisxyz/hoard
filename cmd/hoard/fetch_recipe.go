package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/recipe"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

// runFetchByRecipe fetches what was discovered for a site, at the pace its recipe states
// (a pace flag typed beside it wins for this run) and signed in when the recipe names a
// cookie. The run answers for every address the ledger holds, so its summary and its exit
// code describe the whole capture.
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
	// The ledger is filled by discover. A store that is not there yet is a mistyped
	// directory or a missing first step, and creating it would hide which.
	st, err := store.OpenExisting(opts.storeDir)
	if errors.Is(err, store.ErrNoStore) {
		return reportRunFailure(stderr, fmt.Errorf("%w: run hoard discover with this recipe first", err))
	}
	if err != nil {
		return reportRunFailure(stderr, err)
	}
	defer st.Close()
	addresses, err := everyLedgerAddress(st)
	if err != nil {
		return reportRunFailure(stderr, err)
	}

	pace, replaced := paceForThisRun(site.Fetch, opts)
	// Said before the first request, so that even a log cut short tells whose pace the run
	// was keeping.
	for _, line := range replaced {
		fmt.Fprintln(stderr, line)
	}

	summary, err := fetch.Run(ctx, fetch.RunConfig{
		Store:      st,
		Addresses:  addresses,
		Workers:    pace.Workers,
		Rate:       pace.Rate,
		Timeout:    pace.Timeout,
		UserAgent:  pace.UserAgent,
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

// paceForThisRun is how the site is asked on this run: what the recipe states, except for
// each pace flag that was typed, which wins. A zero value still means nobody stated one, so
// the fetcher's default applies. The lines say, one per replaced value, that the run did
// not keep the recipe's pace; the recipe file itself is never changed.
func paceForThisRun(stated recipe.Fetch, opts fetchOptions) (recipe.Fetch, []string) {
	pace := stated
	var said [4]string
	pace.Workers, said[0] = settleRecipePace(opts.paceFlagWasTyped("--workers"), "--workers", opts.workers, stated.Workers, strconv.Itoa)
	pace.Rate, said[1] = settleRecipePace(opts.paceFlagWasTyped("--rate"), "--rate", opts.rate, stated.Rate, rateWithoutExponent)
	pace.Timeout, said[2] = settleRecipePace(opts.paceFlagWasTyped("--timeout"), "--timeout", opts.timeout, stated.Timeout, time.Duration.String)
	pace.UserAgent, said[3] = settleRecipePace(opts.paceFlagWasTyped("--user-agent"), "--user-agent", opts.userAgent, stated.UserAgent, strconv.Quote)
	return pace, slices.DeleteFunc(said[:], func(line string) bool { return line == "" })
}

// settleRecipePace picks one setting's value, and words the replacement when the flag
// that was typed is the one picked. A typed flag is reported even when its value equals
// the recipe's, because the line says where the value came from, not that it differs.
func settleRecipePace[T comparable](typed bool, flagName string, fromFlag, fromRecipe T, show func(T) string) (T, string) {
	if !typed {
		return fromRecipe, ""
	}
	var unstated T
	if fromRecipe == unstated {
		return fromFlag, fmt.Sprintf("hoard fetch: %s %s from the command line for this run; the recipe states none", flagName, show(fromFlag))
	}
	return fromFlag, fmt.Sprintf("hoard fetch: %s %s from the command line for this run, not the recipe's %s", flagName, show(fromFlag), show(fromRecipe))
}

// A rate is shown the way it is typed, never as 1e+06.
func rateWithoutExponent(rate float64) string {
	return strconv.FormatFloat(rate, 'f', -1, 64)
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
