package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/discover"
	"github.com/gabrielassisxyz/hoard/internal/recipe"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

type discoverOptions struct {
	recipePath string
	storeDir   string
	// paths holds the prefixes --paths gave. It is empty when the flag was not typed, and a
	// typed one is never empty.
	paths []string
	// limit is zero when --limit was not typed, and at least 1 when it was.
	limit  int
	random bool
}

// runDiscoverCommand finds a site's addresses and gives rows in the ledger to the ones the
// flags choose. The store is opened only once the whole list is in hand and something is
// chosen from it, so a discovery that fails leaves nothing behind, not even an empty store.
func runDiscoverCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, exitCode, ok := parseDiscoverArguments(args, stderr)
	if !ok {
		return exitCode
	}
	site, err := recipe.Load(opts.recipePath)
	if err != nil {
		// A recipe that cannot be read is a command that cannot be read: nothing was tried.
		fmt.Fprintf(stderr, "hoard discover: %v\n", err)
		return exitCodeUsage
	}
	found, err := discover.Run(ctx, site)
	if err != nil {
		fmt.Fprintf(stderr, "hoard discover: %v\n", err)
		return exitCodeIncomplete
	}
	chosen := discover.ChosenByPath(found.Addresses, opts.paths)
	if len(chosen) == 0 {
		fmt.Fprintf(stderr, "hoard discover: --paths %s chooses none of the %d addresses found\n",
			strings.Join(opts.paths, ","), len(found.Addresses))
		return exitCodeIncomplete
	}
	record, err := recordDiscoveredAddresses(opts.storeDir, chosen, opts.limit, randomSourceFor(opts))
	if err != nil {
		fmt.Fprintf(stderr, "hoard discover: %v\n", err)
		return exitCodeIncomplete
	}
	fmt.Fprintln(stdout, discoverSummary(len(chosen), record, found.OffSite))
	return exitCodeComplete
}

// discoveryRecord is what recording a discovery did to the ledger.
type discoveryRecord struct {
	// added counts the rows the ledger did not have before.
	added int
	// beyondLimit counts the chosen addresses left without a row because the limit was met.
	beyondLimit int
}

// recordDiscoveredAddresses adds the chosen addresses the limit leaves room for as pending
// rows. A row that exists keeps its state, which is what makes discovering again safe on a
// capture that is half done.
func recordDiscoveredAddresses(storeDir string, chosen []string, limit int, source discover.IntNSource) (discoveryRecord, error) {
	st, err := store.Open(storeDir)
	if err != nil {
		return discoveryRecord{}, err
	}
	defer st.Close()
	held, err := ledgerAddressSet(st)
	if err != nil {
		return discoveryRecord{}, err
	}
	before, err := st.Counts()
	if err != nil {
		return discoveryRecord{}, err
	}
	additions, beyondLimit := discover.ChooseAdditions(chosen, held, limit, source)
	if err := st.AddPending(additions); err != nil {
		return discoveryRecord{}, err
	}
	after, err := st.Counts()
	if err != nil {
		return discoveryRecord{}, err
	}
	return discoveryRecord{added: after.Total() - before.Total(), beyondLimit: beyondLimit}, nil
}

// ledgerAddressSet is every address the ledger has, in any state.
func ledgerAddressSet(st *store.Store) (map[string]struct{}, error) {
	pages, err := st.Pages()
	if err != nil {
		return nil, err
	}
	addresses := make(map[string]struct{}, len(pages))
	for _, page := range pages {
		addresses[page.Address] = struct{}{}
	}
	return addresses, nil
}

// randomSourceFor is seeded from the clock. There is no --seed: nothing needs the same
// sample twice, because what was picked is in the ledger.
func randomSourceFor(opts discoverOptions) discover.IntNSource {
	if !opts.random {
		return nil
	}
	return rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))
}

// discoverSummary says what was found among the chosen addresses. The last part appears
// only when the limit left some of them without a row, so a run with no limit, or one that
// reached everything, prints what it always has.
func discoverSummary(chosenCount int, record discoveryRecord, offSite int) string {
	line := fmt.Sprintf("%d found, %d new, %d off-site", chosenCount, record.added, offSite)
	if record.beyondLimit > 0 {
		line += fmt.Sprintf(", %d beyond the limit", record.beyondLimit)
	}
	return line
}

func parseDiscoverArguments(args []string, stderr io.Writer) (discoverOptions, int, bool) {
	var opts discoverOptions
	flags := flag.NewFlagSet("hoard discover", flag.ContinueOnError)
	flags.SetOutput(stderr)
	declareDiscoverFlags(flags, &opts)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: hoard discover --recipe <file> --store <dir> [--paths <prefixes>] [--limit <n> [--random]]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, exitCodeComplete, false
		}
		// The flag package has already said what was wrong and printed the usage.
		return opts, exitCodeUsage, false
	}
	if problem := discoverArgumentProblem(opts, flags.NArg()); problem != "" {
		fmt.Fprintf(stderr, "hoard discover: %s\n", problem)
		flags.Usage()
		return opts, exitCodeUsage, false
	}
	return opts, exitCodeComplete, true
}

// declareDiscoverFlags ties --paths and --limit to functions so that a value is checked
// where it is read: they are only called for a flag that was typed, which is how "typed
// with nothing in it" is told apart from "not typed".
func declareDiscoverFlags(flags *flag.FlagSet, opts *discoverOptions) {
	flags.StringVar(&opts.recipePath, "recipe", "", "the site's recipe file (required)")
	flags.StringVar(&opts.storeDir, "store", "", "directory that holds the ledger and the raw bodies (required)")
	flags.Func("paths", "record only addresses whose path starts with one of these `prefixes`, separated by commas, each starting with /",
		func(value string) error {
			prefixes, err := discover.ParsePathPrefixes(value)
			if err != nil {
				return err
			}
			opts.paths = prefixes
			return nil
		})
	flags.Func("limit", "record at most `n` of the chosen addresses, counting the ones the store already holds",
		func(value string) error {
			limit, err := strconv.Atoi(value)
			if err != nil || limit < 1 {
				return errors.New("must be a whole number, at least 1")
			}
			opts.limit = limit
			return nil
		})
	flags.BoolVar(&opts.random, "random", false, "with --limit, pick the addresses at random instead of in the order the site lists them")
}

func discoverArgumentProblem(opts discoverOptions, extraArguments int) string {
	switch {
	case extraArguments > 0:
		return "unexpected arguments after the flags"
	case opts.recipePath == "":
		return "--recipe is required"
	case opts.storeDir == "":
		return "--store is required"
	case opts.random && opts.limit == 0:
		return "--random needs --limit: without a limit there is nothing to pick"
	}
	return ""
}
