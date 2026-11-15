package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

type assetsOptions struct {
	storeDir  string
	listPath  string
	workers   int
	rate      float64
	timeout   time.Duration
	userAgent string
}

// runAssetsCommand keeps the images of a list of addresses in the store, apart from the
// pages. The list adds addresses, so like fetch --list it creates the store when none is
// there yet. No cookie is ever sent: images live on whatever host the author chose.
func runAssetsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, exitCode, ok := parseAssetsArguments(args, stderr)
	if !ok {
		return exitCode
	}
	addresses, err := readListFile(opts.listPath)
	if err != nil {
		return reportAssetsFailure(stderr, err)
	}
	st, err := store.Open(opts.storeDir)
	if err != nil {
		return reportAssetsFailure(stderr, err)
	}
	defer st.Close()

	summary, err := fetch.Run(ctx, fetch.RunConfig{
		Store:     st,
		Images:    true,
		Addresses: addresses,
		Workers:   opts.workers,
		Rate:      opts.rate,
		Timeout:   opts.timeout,
		UserAgent: opts.userAgent,
	})
	if err != nil {
		return reportAssetsFailure(stderr, err)
	}
	fmt.Fprintln(stdout, summary)
	if !summary.Complete() {
		return exitCodeIncomplete
	}
	return exitCodeComplete
}

func reportAssetsFailure(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "hoard assets: %v\n", err)
	return exitCodeIncomplete
}

func parseAssetsArguments(args []string, stderr io.Writer) (assetsOptions, int, bool) {
	var opts assetsOptions
	flags := flag.NewFlagSet("hoard assets", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.storeDir, "store", "", "directory that holds the ledger and the bodies (required)")
	flags.StringVar(&opts.listPath, "list", "", "file with one image address per line (required)")
	flags.IntVar(&opts.workers, "workers", fetch.DefaultWorkers, "most requests in flight at once, across all hosts")
	flags.Float64Var(&opts.rate, "rate", fetch.DefaultRate, "requests per second over the whole run, whatever host each goes to")
	flags.DurationVar(&opts.timeout, "timeout", fetch.DefaultTimeout, "time allowed for one request")
	flags.StringVar(&opts.userAgent, "user-agent", fetch.DefaultUserAgent, "User-Agent header sent with every request")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: hoard assets --store <dir> --list <file> [--workers N] [--rate R] [--timeout D] [--user-agent S]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, exitCodeComplete, false
		}
		return opts, exitCodeUsage, false
	}
	if problem := assetsArgumentProblem(opts, flags.NArg()); problem != "" {
		fmt.Fprintf(stderr, "hoard assets: %s\n", problem)
		flags.Usage()
		return opts, exitCodeUsage, false
	}
	return opts, exitCodeComplete, true
}

func assetsArgumentProblem(opts assetsOptions, extraArguments int) string {
	switch {
	case extraArguments > 0:
		return "unexpected arguments after the flags"
	case opts.storeDir == "":
		return "--store is required"
	case opts.listPath == "":
		return "--list is required"
	case opts.workers < 1:
		return "--workers must be at least 1"
	case !(opts.rate > 0) || math.IsInf(opts.rate, 0):
		return "--rate must be a positive number of requests per second"
	case opts.timeout <= 0:
		return "--timeout must be longer than zero"
	case opts.userAgent == "":
		return "--user-agent cannot be empty"
	}
	return ""
}
