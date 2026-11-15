package discover

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// The functions here choose among the addresses a discovery found. They work on the whole
// list after Run has given it, never inside Run: how much of a site one capture takes is a
// choice made when capturing, and a list cut short while it is being read would look
// complete to everything that reads the ledger afterwards.

// ParsePathPrefixes reads the value of --paths: prefixes separated by commas, each
// starting with "/". An empty element is refused instead of skipped, because "/p/," most
// likely lost a prefix and skipping it would choose less than was meant, in silence.
func ParsePathPrefixes(value string) ([]string, error) {
	prefixes := strings.Split(value, ",")
	for _, prefix := range prefixes {
		switch {
		case prefix == "":
			return nil, errors.New("empty prefix: write prefixes separated by single commas")
		case !strings.HasPrefix(prefix, "/"):
			return nil, fmt.Errorf("%q does not start with /", prefix)
		}
	}
	return prefixes, nil
}

// ChosenByPath keeps, in their order, the addresses whose path starts with one of the
// prefixes. With no prefixes it keeps them all.
//
// The comparison is text, on the path in its percent-escaped form, so a prefix writes a
// non-ASCII character as its escape: "/p/" chooses "/p/an-article" and not "/people/x",
// and an escaped slash is not a separator, so "/a%2Fb" is not under "/a/". An address with
// no path stands for "/", as it does when it is requested.
func ChosenByPath(addresses, prefixes []string) []string {
	if len(prefixes) == 0 {
		return addresses
	}
	var chosen []string
	for _, address := range addresses {
		if addressIsUnderAny(address, prefixes) {
			chosen = append(chosen, address)
		}
	}
	return chosen
}

// Run only returns addresses that parse, so one that does not is under no path.
func addressIsUnderAny(address string, prefixes []string) bool {
	parsed, err := url.Parse(address)
	if err != nil {
		return false
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	return slices.ContainsFunc(prefixes, func(prefix string) bool { return strings.HasPrefix(path, prefix) })
}

// IntNSource is what choosing at random asks of a source of randomness. A *rand.Rand from
// math/rand/v2 is one. A test passes one that returns scripted numbers, which is the only
// way to say exactly which addresses a given source selects.
type IntNSource interface {
	// IntN returns a number from 0 up to, not including, n. n is at least 1.
	IntN(n int) int
}

// ChooseAdditions says which of the chosen addresses a run gives a row, and how many it
// leaves without one because of the limit. chosen lists each address once, in the order
// the site lists them, and recorded holds every address the ledger already has, in any
// state.
//
// A limit is the number of chosen addresses the store ends up holding, not the number this
// run adds: the chosen addresses already recorded count toward it and only the difference
// is added. That is what makes asking twice with one limit add nothing, and raising the
// limit add only the next addresses. Recorded addresses that were not chosen count for
// nothing. A limit below 1 means there is none.
//
// With a nil source the additions are the first chosen addresses that are not recorded.
// With a source they are as many of those, each as likely as any other, and still in the
// order the site lists them.
func ChooseAdditions(chosen []string, recorded map[string]struct{}, limit int, source IntNSource) (additions []string, beyondLimit int) {
	unrecorded := make([]string, 0, len(chosen))
	for _, address := range chosen {
		if _, held := recorded[address]; !held {
			unrecorded = append(unrecorded, address)
		}
	}
	if limit < 1 {
		return unrecorded, 0
	}
	room := limit - (len(chosen) - len(unrecorded))
	take := min(max(room, 0), len(unrecorded))
	return pickAmong(unrecorded, take, source), len(unrecorded) - take
}

// pickAmong takes count of the candidates. Taking all of them, or none, asks nothing of
// the source.
func pickAmong(candidates []string, count int, source IntNSource) []string {
	switch {
	case count == len(candidates):
		return candidates
	case count == 0:
		return nil
	case source == nil:
		return candidates[:count]
	}
	return drawWithoutReplacement(candidates, count, source)
}

// drawWithoutReplacement picks count candidates by a partial shuffle: each draw takes one
// of the places not taken yet, so every set of count candidates is as likely as any other.
// The picks come back in the order of the candidates.
func drawWithoutReplacement(candidates []string, count int, source IntNSource) []string {
	places := make([]int, len(candidates))
	for i := range places {
		places[i] = i
	}
	for taken := 0; taken < count; taken++ {
		drawn := taken + source.IntN(len(places)-taken)
		places[taken], places[drawn] = places[drawn], places[taken]
	}
	picked := places[:count]
	slices.Sort(picked)
	addresses := make([]string, count)
	for i, place := range picked {
		addresses[i] = candidates[place]
	}
	return addresses
}
