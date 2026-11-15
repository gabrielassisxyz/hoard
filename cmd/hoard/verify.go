package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/gabrielassisxyz/hoard/internal/recipe"
	"github.com/gabrielassisxyz/hoard/internal/store"
	"github.com/gabrielassisxyz/hoard/internal/verify"
)

type verifyOptions struct {
	recipePath string
	storeDir   string
}

// runVerifyCommand judges a capture by its recipe's criterion. It reads and never writes:
// the store has to exist already.
func runVerifyCommand(args []string, stdout, stderr io.Writer) int {
	opts, exitCode, ok := parseVerifyArguments(args, stderr)
	if !ok {
		return exitCode
	}
	site, err := recipe.Load(opts.recipePath)
	if err != nil {
		fmt.Fprintf(stderr, "hoard verify: %v\n", err)
		return exitCodeUsage
	}
	st, err := store.OpenExisting(opts.storeDir)
	if err != nil {
		fmt.Fprintf(stderr, "hoard verify: %v\n", err)
		return exitCodeIncomplete
	}
	defer st.Close()
	report, err := verify.Check(st, site.Verify)
	if err != nil {
		fmt.Fprintf(stderr, "hoard verify: %v\n", err)
		return exitCodeIncomplete
	}
	report.Print(stdout)
	if !report.Met() {
		return exitCodeIncomplete
	}
	return exitCodeComplete
}

func parseVerifyArguments(args []string, stderr io.Writer) (verifyOptions, int, bool) {
	var opts verifyOptions
	flags := flag.NewFlagSet("hoard verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.recipePath, "recipe", "", "the site's recipe file, whose [verify] table is the criterion (required)")
	flags.StringVar(&opts.storeDir, "store", "", "directory that holds the ledger and the raw bodies (required)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: hoard verify --recipe <file> --store <dir>")
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
		fmt.Fprintf(stderr, "hoard verify: %s\n", problem)
		flags.Usage()
		return opts, exitCodeUsage, false
	}
	return opts, exitCodeComplete, true
}
