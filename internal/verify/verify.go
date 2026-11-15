// Package verify checks a capture against what its recipe says a complete one looks like.
//
// A count of answered requests cannot tell a capture of articles from a capture of the
// page a site shows instead of them: a paywall answers 200 too. The criterion is what
// tells them apart.
package verify

import (
	"bytes"
	"fmt"
	"io"

	"github.com/gabrielassisxyz/hoard/internal/recipe"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

// Report is what a check found. The slices hold addresses in ledger order.
type Report struct {
	Held, Failed, Pending int

	MinPages     int
	MinBodyBytes int64
	// TooSmall are held pages whose body is under MinBodyBytes.
	TooSmall []string
	// Forbidden has one entry per forbidden text found in at least one held page.
	Forbidden []ForbiddenFinding
}

// ForbiddenFinding is the held pages that contain one of the recipe's forbidden texts.
type ForbiddenFinding struct {
	Text      string
	Addresses []string
}

// TooFewPages says the capture holds fewer pages than the criterion asks for.
func (r Report) TooFewPages() bool {
	return r.Held < r.MinPages
}

// Met is true when no part of the criterion is broken. Failed and pending addresses do
// not enter into it: whether everything asked for was obtained is the fetch's own answer,
// and a site with one dead link would otherwise never verify.
func (r Report) Met() bool {
	return !r.TooFewPages() && len(r.TooSmall) == 0 && len(r.Forbidden) == 0
}

// Check reads the store and judges it by criterion.
func Check(st *store.Store, criterion recipe.Verify) (Report, error) {
	counts, err := st.Counts()
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Held: counts.Held, Failed: counts.Failed, Pending: counts.Pending,
		MinPages: criterion.MinPages, MinBodyBytes: criterion.MinBodyBytes,
	}
	pages, err := st.Pages()
	if err != nil {
		return Report{}, err
	}
	containing := make([][]string, len(criterion.Forbidden))
	for _, page := range pages {
		if page.State != store.StateOK {
			continue
		}
		if page.SizeBytes < criterion.MinBodyBytes {
			report.TooSmall = append(report.TooSmall, page.Address)
		}
		if len(criterion.Forbidden) == 0 {
			// The size is in the ledger; only a forbidden text needs the body read.
			continue
		}
		body, err := st.ReadBody(page.SHA256)
		if err != nil {
			return Report{}, fmt.Errorf("read the body of %s: %w", page.Address, err)
		}
		for position, text := range criterion.Forbidden {
			if bytes.Contains(body, []byte(text)) {
				containing[position] = append(containing[position], page.Address)
			}
		}
	}
	for position, addresses := range containing {
		if len(addresses) > 0 {
			report.Forbidden = append(report.Forbidden, ForbiddenFinding{Text: criterion.Forbidden[position], Addresses: addresses})
		}
	}
	return report, nil
}

// How many addresses are named under one broken part of the criterion. A badly broken
// capture has thousands, and the count above them already says how bad it is.
const addressesShownPerFinding = 10

// Print writes the verdict and, for each broken part, how many pages break it and the
// first few of them.
func (r Report) Print(w io.Writer) {
	if r.Met() {
		fmt.Fprintf(w, "verify: ok, %d pages held%s\n", r.Held, r.unsettledNote(" (", ")"))
		return
	}
	fmt.Fprintln(w, "verify: not met")
	if r.TooFewPages() {
		fmt.Fprintf(w, "  pages held: %d, at least %d required\n", r.Held, r.MinPages)
	}
	if len(r.TooSmall) > 0 {
		fmt.Fprintf(w, "  smaller than %d bytes: %d pages\n", r.MinBodyBytes, len(r.TooSmall))
		printAddresses(w, r.TooSmall)
	}
	for _, finding := range r.Forbidden {
		fmt.Fprintf(w, "  containing %q: %d pages\n", finding.Text, len(finding.Addresses))
		printAddresses(w, finding.Addresses)
	}
	if note := r.unsettledNote("  not part of the criterion: ", ""); note != "" {
		fmt.Fprintln(w, note)
	}
}

func (r Report) unsettledNote(before, after string) string {
	if r.Failed == 0 && r.Pending == 0 {
		return ""
	}
	return fmt.Sprintf("%s%d failed, %d pending%s", before, r.Failed, r.Pending, after)
}

func printAddresses(w io.Writer, addresses []string) {
	shown := addresses
	if len(shown) > addressesShownPerFinding {
		shown = shown[:addressesShownPerFinding]
	}
	for _, address := range shown {
		fmt.Fprintf(w, "    %s\n", address)
	}
	if rest := len(addresses) - len(shown); rest > 0 {
		fmt.Fprintf(w, "    and %d more\n", rest)
	}
}
