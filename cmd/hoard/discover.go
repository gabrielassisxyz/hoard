package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/gabrielassisxyz/hoard/internal/discover"
	"github.com/gabrielassisxyz/hoard/internal/recipe"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

type discoverOptions struct {
	recipePath string
	storeDir   string
}

// runDiscoverCommand finds a site's addresses and gives each a row in the ledger. The
// store is opened only once the whole list is in hand, so a discovery that fails leaves
// nothing behind, not even an empty store.
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
	added, err := recordDiscoveredAddresses(opts.storeDir, found.Addresses)
	if err != nil {
		fmt.Fprintf(stderr, "hoard discover: %v\n", err)
		return exitCodeIncomplete
	}
	fmt.Fprintf(stdout, "%d found, %d new, %d off-site\n", len(found.Addresses), added, found.OffSite)
	return exitCodeComplete
}

// recordDiscoveredAddresses adds the addresses as pending rows and says how many the
// ledger did not already have. A row that exists keeps its state, which is what makes
// discovering again safe on a capture that is half done.
func recordDiscoveredAddresses(storeDir string, addresses []string) (int, error) {
	st, err := store.Open(storeDir)
	if err != nil {
		return 0, err
	}
	defer st.Close()
	before, err := st.Counts()
	if err != nil {
		return 0, err
	}
	if err := st.AddPending(addresses); err != nil {
		return 0, err
	}
	after, err := st.Counts()
	if err != nil {
		return 0, err
	}
	return after.Total() - before.Total(), nil
}

func parseDiscoverArguments(args []string, stderr io.Writer) (discoverOptions, int, bool) {
	var opts discoverOptions
	flags := flag.NewFlagSet("hoard discover", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.recipePath, "recipe", "", "the site's recipe file (required)")
	flags.StringVar(&opts.storeDir, "store", "", "directory that holds the ledger and the raw bodies (required)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: hoard discover --recipe <file> --store <dir>")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, exitCodeComplete, false
		}
		return opts, exitCodeUsage, false
	}
	problem := ""
	switch {
	case flags.NArg() > 0:
		problem = "unexpected arguments after the flags"
	case opts.recipePath == "":
		problem = "--recipe is required"
	case opts.storeDir == "":
		problem = "--store is required"
	}
	if problem != "" {
		fmt.Fprintf(stderr, "hoard discover: %s\n", problem)
		flags.Usage()
		return opts, exitCodeUsage, false
	}
	return opts, exitCodeComplete, true
}
