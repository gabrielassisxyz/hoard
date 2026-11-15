// Command hoard captures web pages. For now it does one thing: fetch a list of addresses
// into a raw store, under one rate for the whole run.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

// 0 only when every listed address is held. A run that took less than it was asked for is
// not a success, so a script can stop on the exit code alone.
const (
	exitCodeComplete   = 0
	exitCodeIncomplete = 1
	exitCodeUsage      = 2
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := hoardMain(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// hoardMain is main without the process around it, so the command's behaviour does not
// depend on os.Args, os.Exit or the real standard streams.
func hoardMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printTopLevelUsage(stderr)
		return exitCodeUsage
	}
	switch args[0] {
	case "discover":
		return runDiscoverCommand(ctx, args[1:], stdout, stderr)
	case "fetch":
		return runFetchCommand(ctx, args[1:], stdout, stderr)
	case "list":
		return runListCommand(args[1:], stdout, stderr)
	case "status":
		return runStatusCommand(args[1:], stdout, stderr)
	case "verify":
		return runVerifyCommand(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printTopLevelUsage(stdout)
		return exitCodeComplete
	default:
		fmt.Fprintf(stderr, "hoard: unknown command %q\n", args[0])
		printTopLevelUsage(stderr)
		return exitCodeUsage
	}
}

func printTopLevelUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: hoard <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  discover  find a site's addresses and record them in a store")
	fmt.Fprintln(w, "  fetch     fetch a list of addresses, or what a recipe discovered, into a raw store")
	fmt.Fprintln(w, "  list      list the pages a store holds, one JSON object per line")
	fmt.Fprintln(w, "  status    say how many addresses a store holds, and why the failed ones failed")
	fmt.Fprintln(w, "  verify    check a store against the criterion its recipe states")
}

type fetchOptions struct {
	storeDir   string
	listPath   string
	recipePath string
	// paceFlagsGiven names the pace flags that were typed, as opposed to left at their
	// defaults: with a recipe, only those replace the recipe's value.
	paceFlagsGiven []string
	workers        int
	rate           float64
	timeout        time.Duration
	userAgent      string
}

// paceFlagWasTyped takes the flag as the person sees it, with its dashes.
func (o fetchOptions) paceFlagWasTyped(flagName string) bool {
	return slices.Contains(o.paceFlagsGiven, flagName)
}

func runFetchCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, exitCode, ok := parseFetchArguments(args, stderr)
	if !ok {
		return exitCode
	}
	if opts.recipePath != "" {
		return runFetchByRecipe(ctx, opts, stdout, stderr)
	}
	addresses, err := readListFile(opts.listPath)
	if err != nil {
		return reportRunFailure(stderr, err)
	}
	st, err := store.Open(opts.storeDir)
	if err != nil {
		return reportRunFailure(stderr, err)
	}
	defer st.Close()

	summary, err := fetch.Run(ctx, fetch.RunConfig{
		Store:     st,
		Addresses: addresses,
		Workers:   opts.workers,
		Rate:      opts.rate,
		Timeout:   opts.timeout,
		UserAgent: opts.userAgent,
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

// reportRunFailure is for a run that could not be carried out at all, as opposed to a
// command line that could not be read: the exit code says the run did not get what it was
// asked for.
func reportRunFailure(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "hoard fetch: %v\n", err)
	return exitCodeIncomplete
}

func readListFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	addresses, err := fetch.ParseList(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return addresses, nil
}

// parseFetchArguments reads the flags. When it returns false the command is over, and the
// exit code to use comes with it: 2 for anything that could not be read, 0 for a request
// for help.
func parseFetchArguments(args []string, stderr io.Writer) (fetchOptions, int, bool) {
	var opts fetchOptions
	flags := flag.NewFlagSet("hoard fetch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.storeDir, "store", "", "directory that holds the ledger and the raw bodies (required)")
	flags.StringVar(&opts.listPath, "list", "", "file with one address per line")
	flags.StringVar(&opts.recipePath, "recipe", "", "the site's recipe file: fetch what was discovered for it, at its pace")
	flags.IntVar(&opts.workers, "workers", fetch.DefaultWorkers, "most requests in flight at once, across all hosts")
	flags.Float64Var(&opts.rate, "rate", fetch.DefaultRate, "requests per second over the whole run, whatever host each goes to")
	flags.DurationVar(&opts.timeout, "timeout", fetch.DefaultTimeout, "time allowed for one request")
	flags.StringVar(&opts.userAgent, "user-agent", fetch.DefaultUserAgent, "User-Agent header sent with every request")
	flags.Usage = func() { printFetchUsage(stderr, flags) }

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, exitCodeComplete, false
		}
		// The flag package has already said what was wrong and printed the usage.
		return opts, exitCodeUsage, false
	}
	flags.Visit(func(given *flag.Flag) {
		switch given.Name {
		case "workers", "rate", "timeout", "user-agent":
			opts.paceFlagsGiven = append(opts.paceFlagsGiven, "--"+given.Name)
		}
	})
	if problem := fetchArgumentProblem(opts, flags.NArg()); problem != "" {
		fmt.Fprintf(stderr, "hoard fetch: %s\n", problem)
		flags.Usage()
		return opts, exitCodeUsage, false
	}
	return opts, exitCodeComplete, true
}

func fetchArgumentProblem(opts fetchOptions, extraArguments int) string {
	switch {
	case extraArguments > 0:
		return "unexpected arguments after the flags"
	case opts.storeDir == "":
		return "--store is required"
	case opts.listPath == "" && opts.recipePath == "":
		return "--list is required, or --recipe for a site that has one"
	case opts.listPath != "" && opts.recipePath != "":
		return "--list and --recipe cannot be combined: a run fetches one or the other"
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

func printFetchUsage(w io.Writer, flags *flag.FlagSet) {
	fmt.Fprintln(w, "usage: hoard fetch --store <dir> --list <file> [--workers N] [--rate R] [--timeout D] [--user-agent S]")
	fmt.Fprintln(w, "       hoard fetch --store <dir> --recipe <file> [--workers N] [--rate R] [--timeout D] [--user-agent S]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "With --recipe the pace is the recipe's, except that a pace flag typed on the command line")
	fmt.Fprintln(w, "wins over the recipe for that one run, and each value it replaces is reported on standard")
	fmt.Fprintln(w, "error. A flag that is not typed leaves the recipe's value, or the default shown below when")
	fmt.Fprintln(w, "the recipe states none.")
	fmt.Fprintln(w)
	flags.PrintDefaults()
}
