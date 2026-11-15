package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/store"
)

type listOptions struct {
	storeDir string
	asJSON   bool
	assets   bool
}

// listedPage is one line of `hoard list --json`. The program that reads the lines takes
// these keys as its whole contract, so the set and the order change on purpose or not at all.
type listedPage struct {
	Address      string `json:"address"`
	FinalAddress string `json:"final_address"`
	HTTPStatus   int    `json:"http_status"`
	FetchedAt    string `json:"fetched_at"`
	ContentType  string `json:"content_type"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
	BodyPath     string `json:"body_path"`
}

// listedAsset is one line of `hoard list --assets --json`, with the same promise as a page's.
type listedAsset struct {
	Address      string `json:"address"`
	FinalAddress string `json:"final_address"`
	ContentType  string `json:"content_type"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
	FilePath     string `json:"file_path"`
}

// runListCommand prints what a store holds. It never creates anything: a mistyped store
// directory has to be an error here, not an empty store left behind.
func runListCommand(args []string, stdout, stderr io.Writer) int {
	opts, exitCode, ok := parseListArguments(args, stderr)
	if !ok {
		return exitCode
	}
	st, err := store.OpenExisting(opts.storeDir)
	if err != nil {
		return reportListFailure(stderr, err)
	}
	defer st.Close()

	if opts.assets {
		return listAssets(st, stdout, stderr)
	}
	pages, err := st.Pages()
	if err != nil {
		return reportListFailure(stderr, err)
	}
	if err := writeListedPages(stdout, opts.storeDir, pages); err != nil {
		return reportListFailure(stderr, err)
	}
	return exitCodeComplete
}

func listAssets(st *store.Store, stdout, stderr io.Writer) int {
	assets, err := st.AssetLedger().Pages()
	if err != nil {
		return reportListFailure(stderr, err)
	}
	if err := writeListedAssets(stdout, st, assets); err != nil {
		return reportListFailure(stderr, err)
	}
	return exitCodeComplete
}

// writeListedAssets prints the held images, in address order. The path is the store's own
// answer, since the extension in it depends on rules that live there.
func writeListedAssets(w io.Writer, st *store.Store, assets []store.Page) error {
	out := bufio.NewWriter(w)
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	for _, asset := range assets {
		if asset.State != store.StateOK {
			continue
		}
		finalAddress := asset.FinalAddress
		if finalAddress == "" {
			finalAddress = asset.Address
		}
		line := listedAsset{
			Address:      asset.Address,
			FinalAddress: finalAddress,
			ContentType:  asset.ContentType,
			SHA256:       asset.SHA256,
			SizeBytes:    asset.SizeBytes,
			FilePath:     st.AssetPath(asset),
		}
		if err := encoder.Encode(line); err != nil {
			return fmt.Errorf("write the listing: %w", err)
		}
	}
	if err := out.Flush(); err != nil {
		return fmt.Errorf("write the listing: %w", err)
	}
	return nil
}

func reportListFailure(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "hoard list: %v\n", err)
	return exitCodeIncomplete
}

// writeListedPages prints the rows that hold a body, in the order they come in. Rows still
// pending or failed have no body to hand to whatever reads this next, so they are not lines.
func writeListedPages(w io.Writer, storeDir string, pages []store.Page) error {
	absStoreDir, err := filepath.Abs(storeDir)
	if err != nil {
		return fmt.Errorf("resolve store directory: %w", err)
	}
	out := bufio.NewWriter(w)
	encoder := json.NewEncoder(out)
	// Addresses carry & in their query strings. & is valid JSON, and unreadable to the
	// person who runs this without a pipe.
	encoder.SetEscapeHTML(false)
	for _, page := range pages {
		if page.State != store.StateOK {
			continue
		}
		if err := encoder.Encode(listedPageOf(page, absStoreDir)); err != nil {
			return fmt.Errorf("write the listing: %w", err)
		}
	}
	if err := out.Flush(); err != nil {
		return fmt.Errorf("write the listing: %w", err)
	}
	return nil
}

func listedPageOf(page store.Page, absStoreDir string) listedPage {
	finalAddress := page.FinalAddress
	if finalAddress == "" {
		finalAddress = page.Address
	}
	return listedPage{
		Address:      page.Address,
		FinalAddress: finalAddress,
		HTTPStatus:   page.HTTPStatus,
		FetchedAt:    page.FetchedAt.UTC().Format(time.RFC3339),
		ContentType:  page.ContentType,
		SHA256:       page.SHA256,
		SizeBytes:    page.SizeBytes,
		// The layout is the store's (see its package comment). It is repeated here instead of
		// exported because the store's API is fixed for this change; the tests read every
		// printed path back, so a layout that moves fails there.
		BodyPath: filepath.Join(absStoreDir, "raw", page.SHA256[:2], page.SHA256+".zst"),
	}
}

// parseListArguments reads the flags. When it returns false the command is over, and the
// exit code to use comes with it: 2 for anything that could not be read, 0 for a request
// for help.
func parseListArguments(args []string, stderr io.Writer) (listOptions, int, bool) {
	var opts listOptions
	flags := flag.NewFlagSet("hoard list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.storeDir, "store", "", "directory that holds the ledger and the raw bodies (required)")
	flags.BoolVar(&opts.asJSON, "json", false, "print one JSON object per stored page (required: no other format exists yet)")
	flags.BoolVar(&opts.assets, "assets", false, "list the images the store holds instead of its pages")
	flags.Usage = func() { printListUsage(stderr, flags) }

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, exitCodeComplete, false
		}
		// The flag package has already said what was wrong and printed the usage.
		return opts, exitCodeUsage, false
	}
	if problem := listArgumentProblem(opts, flags.NArg()); problem != "" {
		fmt.Fprintf(stderr, "hoard list: %s\n", problem)
		flags.Usage()
		return opts, exitCodeUsage, false
	}
	return opts, exitCodeComplete, true
}

func listArgumentProblem(opts listOptions, extraArguments int) string {
	switch {
	case extraArguments > 0:
		return "unexpected arguments after the flags"
	case opts.storeDir == "":
		return "--store is required"
	case !opts.asJSON:
		return "--json is required"
	}
	return ""
}

func printListUsage(w io.Writer, flags *flag.FlagSet) {
	fmt.Fprintln(w, "usage: hoard list --store <dir> --json")
	fmt.Fprintln(w, "       hoard list --store <dir> --assets --json")
	flags.PrintDefaults()
}
