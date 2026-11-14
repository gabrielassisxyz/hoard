// Package fetch brings a list of addresses home. It requests each one under a ceiling on
// requests in flight and a pace per host, keeps every body raw in a store, and records in
// the store's ledger what became of each address.
//
// Fetching is all it does. Nothing here parses, cleans or rewrites a page: everything
// readable is derived later from the bodies this package stored.
package fetch

import (
	"bufio"
	"io"
	"strings"
)

// Longer than any address a person would list, and a bound so that a file with no line
// breaks cannot make the scanner buffer all of it.
const maxListLineBytes = 1 << 20

// ParseList reads one address per line. Blank lines and lines starting with '#' are
// skipped. Addresses are returned as written, trimmed of surrounding space; whether an
// address can be fetched is for Run to decide, so one bad line costs one row and not the
// whole list.
func ParseList(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), maxListLineBytes)
	var addresses []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		addresses = append(addresses, line)
	}
	return addresses, scanner.Err()
}
