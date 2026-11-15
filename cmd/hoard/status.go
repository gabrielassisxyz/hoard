package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/gabrielassisxyz/hoard/internal/store"
)

// runStatusCommand says where a capture stands without asking the site for anything: how
// many addresses are held, failed and still waiting, and why each failed one failed. It
// takes no recipe, since nothing it prints comes from one.
func runStatusCommand(args []string, stdout, stderr io.Writer) int {
	storeDir, exitCode, ok := parseStatusArguments(args, stderr)
	if !ok {
		return exitCode
	}
	st, err := store.OpenExisting(storeDir)
	if err != nil {
		fmt.Fprintf(stderr, "hoard status: %v\n", err)
		return exitCodeIncomplete
	}
	defer st.Close()
	counts, err := st.Counts()
	if err != nil {
		fmt.Fprintf(stderr, "hoard status: %v\n", err)
		return exitCodeIncomplete
	}
	pages, err := st.Pages()
	if err != nil {
		fmt.Fprintf(stderr, "hoard status: %v\n", err)
		return exitCodeIncomplete
	}
	assetCounts, err := st.AssetLedger().Counts()
	if err != nil {
		fmt.Fprintf(stderr, "hoard status: %v\n", err)
		return exitCodeIncomplete
	}
	fmt.Fprintf(stdout, "%d held, %d failed, %d pending\n", counts.Held, counts.Failed, counts.Pending)
	fmt.Fprintf(stdout, "assets: %d held, %d failed, %d pending\n", assetCounts.Held, assetCounts.Failed, assetCounts.Pending)
	for _, page := range pages {
		if page.State == store.StateFailed {
			fmt.Fprintf(stdout, "failed: %s: %s\n", page.Address, page.LastError)
		}
	}
	// A report is not a verdict: whether the capture is good enough is verify's answer.
	return exitCodeComplete
}

func parseStatusArguments(args []string, stderr io.Writer) (string, int, bool) {
	var storeDir string
	flags := flag.NewFlagSet("hoard status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&storeDir, "store", "", "directory that holds the ledger and the raw bodies (required)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: hoard status --store <dir>")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", exitCodeComplete, false
		}
		return "", exitCodeUsage, false
	}
	problem := ""
	switch {
	case flags.NArg() > 0:
		problem = "unexpected arguments after the flags"
	case storeDir == "":
		problem = "--store is required"
	}
	if problem != "" {
		fmt.Fprintf(stderr, "hoard status: %s\n", problem)
		flags.Usage()
		return "", exitCodeUsage, false
	}
	return storeDir, exitCodeComplete, true
}
