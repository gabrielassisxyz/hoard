package discover

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The command seeds a *rand.Rand and hands it over as the source; this stops compiling if
// that ever stops working.
var _ IntNSource = (*rand.Rand)(nil)

// selectionSiteAddress is an address on a synthetic site.
func selectionSiteAddress(path string) string {
	return "https://selection.test" + path
}

func selectionSiteAddresses(paths ...string) []string {
	addresses := make([]string, len(paths))
	for i, path := range paths {
		addresses[i] = selectionSiteAddress(path)
	}
	return addresses
}

// selectionTenChosen is ten addresses in an order that is neither alphabetical nor by
// section, so "the first ones" cannot be mistaken for either.
func selectionTenChosen() []string {
	return selectionSiteAddresses("/s/j", "/s/c", "/s/h", "/s/a", "/s/f", "/s/b", "/s/i", "/s/d", "/s/g", "/s/e")
}

func selectionSetOf(addresses ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		set[address] = struct{}{}
	}
	return set
}

func selectionWantAddresses(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("addresses:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestParsePathPrefixesReadsCommaSeparatedPrefixesThatStartWithASlash(t *testing.T) {
	valid := []struct {
		value string
		want  []string
	}{
		{"/p/", []string{"/p/"}},
		{"/p/,/essays/", []string{"/p/", "/essays/"}},
		{"/", []string{"/"}},
		{"/p,/p/a", []string{"/p", "/p/a"}},
	}
	for _, c := range valid {
		got, err := ParsePathPrefixes(c.value)
		if err != nil {
			t.Errorf("ParsePathPrefixes(%q): %v", c.value, err)
			continue
		}
		selectionWantAddresses(t, got, c.want...)
	}
}

func TestParsePathPrefixesRefusesAnEmptyElementAndOneThatDoesNotStartWithASlash(t *testing.T) {
	refused := []struct{ value, says string }{
		{"", "empty prefix"},
		{"/p/,", "empty prefix"},
		{",/p/", "empty prefix"},
		{"/p/,,/e/", "empty prefix"},
		{"p/", `"p/" does not start with /`},
		{"/p/,essays/", `"essays/" does not start with /`},
		{" /p/", `" /p/" does not start with /`},
		{"https://selection.test/p/", `"https://selection.test/p/" does not start with /`},
	}
	for _, c := range refused {
		prefixes, err := ParsePathPrefixes(c.value)
		if err == nil {
			t.Errorf("ParsePathPrefixes(%q) = %q, want an error", c.value, prefixes)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("ParsePathPrefixes(%q) error %q does not say %q", c.value, err, c.says)
		}
	}
}

func TestChosenByPathKeepsWhatStartsWithAPrefixAndNothingElse(t *testing.T) {
	addresses := selectionSiteAddresses("/p/an-article", "/people/x", "/essays/one", "/p", "/essays/", "/notes/a")
	got := ChosenByPath(addresses, []string{"/p/", "/essays/"})
	selectionWantAddresses(t, got, selectionSiteAddress("/p/an-article"), selectionSiteAddress("/essays/one"), selectionSiteAddress("/essays/"))
}

func TestChosenByPathComparesPlainText(t *testing.T) {
	addresses := selectionSiteAddresses("/p/an-article", "/people/x", "/p", "/notes/a")
	got := ChosenByPath(addresses, []string{"/p"})
	selectionWantAddresses(t, got, selectionSiteAddress("/p/an-article"), selectionSiteAddress("/people/x"), selectionSiteAddress("/p"))
}

func TestChosenByPathKeepsTheOrderOfTheAddressesNotOfThePrefixes(t *testing.T) {
	addresses := selectionSiteAddresses("/p/1", "/essays/1", "/p/2", "/essays/2")
	got := ChosenByPath(addresses, []string{"/essays/", "/p/"})
	selectionWantAddresses(t, got, addresses...)
}

func TestChosenByPathWithoutPrefixesKeepsEverything(t *testing.T) {
	addresses := selectionSiteAddresses("/p/1", "/essays/1", "/")
	selectionWantAddresses(t, ChosenByPath(addresses, nil), addresses...)
}

func TestChosenByPathLooksOnlyAtThePath(t *testing.T) {
	addresses := []string{
		selectionSiteAddress("/notes?section=/p/2"),
		selectionSiteAddress("/notes#/p/3"),
		selectionSiteAddress("/p/4?page=2#top"),
		"https://selection.test:8443/p/5",
		"https://user@selection.test/p/6",
		"https://selection.test/p/",
	}
	got := ChosenByPath(addresses, []string{"/p/"})
	selectionWantAddresses(t, got, addresses[2], addresses[3], addresses[4], addresses[5])
}

func TestChosenByPathReadsAnAddressWithNoPathAsTheRoot(t *testing.T) {
	addresses := []string{"https://selection.test", "https://selection.test?lang=en", "https://selection.test/", selectionSiteAddress("/p/1")}
	selectionWantAddresses(t, ChosenByPath(addresses, []string{"/"}), addresses...)
	selectionWantAddresses(t, ChosenByPath(addresses, []string{"/p/"}), addresses[3])
}

func TestChosenByPathTakesAnEscapedSlashForPartOfASegment(t *testing.T) {
	addresses := selectionSiteAddresses("/a%2Fb/c", "/a/b/c", "/caf%C3%A9/x")
	selectionWantAddresses(t, ChosenByPath(addresses, []string{"/a/"}), addresses[1])
	selectionWantAddresses(t, ChosenByPath(addresses, []string{"/a%2Fb/"}), addresses[0])
	selectionWantAddresses(t, ChosenByPath(addresses, []string{"/caf%C3%A9/"}), addresses[2])
}

func TestChosenByPathNeverChoosesAnAddressItCannotRead(t *testing.T) {
	addresses := []string{"https://selection.test/%zz/p/", selectionSiteAddress("/p/1")}
	selectionWantAddresses(t, ChosenByPath(addresses, []string{"/"}), addresses[1])
}

func TestChooseAdditionsInOrderTopsUpToTheLimit(t *testing.T) {
	chosen := selectionTenChosen()
	cases := []struct {
		name        string
		recorded    []string
		limit       int
		additions   []string
		beyondLimit int
	}{
		{"no limit adds everything", nil, 0, chosen, 0},
		{"a limit takes the first in the site's order", nil, 3, chosen[:3], 7},
		{"asking again adds nothing", chosen[:3], 3, nil, 7},
		{"a larger limit adds the next ones", chosen[:3], 5, chosen[3:5], 5},
		{"a limit beyond the list takes it all", nil, 50, chosen, 0},
		{"a limit exactly the list takes it all", chosen[:4], 10, chosen[4:], 0},
		{"recorded addresses anywhere in the list count", []string{chosen[2], chosen[5]}, 4, []string{chosen[0], chosen[1]}, 6},
		{"a limit below what is recorded adds nothing", chosen[:4], 2, nil, 6},
		{"nothing left to add", chosen, 5, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			additions, beyondLimit := ChooseAdditions(chosen, selectionSetOf(c.recorded...), c.limit, nil)
			selectionWantAddresses(t, additions, c.additions...)
			if beyondLimit != c.beyondLimit {
				t.Errorf("beyondLimit = %d, want %d", beyondLimit, c.beyondLimit)
			}
		})
	}
}

func TestChooseAdditionsCountsOnlyTheChosenAddressesTowardTheLimit(t *testing.T) {
	essays := selectionSiteAddresses("/e/1", "/e/2", "/e/3", "/e/4", "/e/5")
	held := selectionSiteAddresses("/p/1", "/p/2", "/e/1", "/e/2", "/e/3")
	additions, beyondLimit := ChooseAdditions(essays, selectionSetOf(held...), 4, nil)
	selectionWantAddresses(t, additions, essays[3])
	if beyondLimit != 1 {
		t.Errorf("beyondLimit = %d, want 1", beyondLimit)
	}
}

func TestChooseAdditionsLeavesItsArgumentsAlone(t *testing.T) {
	chosen := selectionTenChosen()
	recorded := selectionSetOf(chosen[1], chosen[4])
	ChooseAdditions(chosen, recorded, 6, nil)
	ChooseAdditions(chosen, recorded, 6, rand.New(rand.NewPCG(7, 7)))
	selectionWantAddresses(t, chosen, selectionTenChosen()...)
	if len(recorded) != 2 {
		t.Errorf("recorded has %d addresses after the call, want 2", len(recorded))
	}
}

// selectionScriptedSource answers IntN with the next number of its script, and remembers the bound
// each answer was asked for.
type selectionScriptedSource struct {
	t      *testing.T
	script []int
	bounds []int
}

func (s *selectionScriptedSource) IntN(n int) int {
	s.t.Helper()
	s.bounds = append(s.bounds, n)
	if len(s.script) == 0 {
		s.t.Fatalf("the source was asked for draw number %d and the script has no more", len(s.bounds))
	}
	next := s.script[0]
	s.script = s.script[1:]
	if next < 0 || next >= n {
		s.t.Fatalf("the script gives %d for a draw below %d", next, n)
	}
	return next
}

func TestChooseAdditionsAtRandomReturnsWhatTheSourceSelects(t *testing.T) {
	chosen := selectionSiteAddresses("/a", "/b", "/c", "/d", "/e", "/f")
	cases := []struct {
		name       string
		recorded   []string
		limit      int
		script     []int
		bounds     []int
		additions  []string
		beyondLeft int
	}{
		// Places 0..5, drawing 5, then 3 of the 5 left, then 0 of the 4 left: 5 and 4 and 2.
		{"a source that reaches for the end", nil, 3, []int{5, 3, 0}, []int{6, 5, 4}, []string{"/c", "/e", "/f"}, 3},
		{"a source that reaches for the start", nil, 3, []int{0, 0, 0}, []int{6, 5, 4}, []string{"/a", "/b", "/c"}, 3},
		// The second draw takes the last of five places, which holds the place the first draw moved out of the front: /a.
		{"a source that takes the last place left each time", nil, 2, []int{5, 4}, []int{6, 5}, []string{"/a", "/f"}, 4},
		// Four are not recorded, so places 0..3 are /c../f, and two are wanted: 3, then 0 of the 3 left.
		{"recorded addresses are never drawn", []string{"/a", "/b"}, 4, []int{3, 0}, []int{4, 3}, []string{"/d", "/f"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			source := &selectionScriptedSource{t: t, script: c.script}
			additions, beyondLimit := ChooseAdditions(chosen, selectionSetOf(selectionSiteAddresses(c.recorded...)...), c.limit, source)
			selectionWantAddresses(t, additions, selectionSiteAddresses(c.additions...)...)
			if beyondLimit != c.beyondLeft {
				t.Errorf("beyondLimit = %d, want %d", beyondLimit, c.beyondLeft)
			}
			if !slices.Equal(source.bounds, c.bounds) {
				t.Errorf("the source was asked for draws below %v, want %v", source.bounds, c.bounds)
			}
			if len(source.script) != 0 {
				t.Errorf("%d numbers of the script were never asked for", len(source.script))
			}
		})
	}
}

func TestChooseAdditionsAtRandomAsksNothingOfTheSourceWhenEverythingIsTaken(t *testing.T) {
	chosen := selectionSiteAddresses("/a", "/b", "/c", "/d")
	for _, limit := range []int{4, 5, 100} {
		source := &selectionScriptedSource{t: t}
		additions, beyondLimit := ChooseAdditions(chosen, nil, limit, source)
		selectionWantAddresses(t, additions, chosen...)
		if beyondLimit != 0 {
			t.Errorf("limit %d: beyondLimit = %d, want 0", limit, beyondLimit)
		}
	}
	source := &selectionScriptedSource{t: t}
	additions, beyondLimit := ChooseAdditions(chosen, selectionSetOf(chosen[0], chosen[1]), 2, source)
	selectionWantAddresses(t, additions)
	if beyondLimit != 2 {
		t.Errorf("limit met: beyondLimit = %d, want 2", beyondLimit)
	}
}

// Whatever the generator draws, the additions are exactly as many as the limit leaves room
// for, are none of the addresses already recorded, and keep the site's order.
func TestChooseAdditionsAtRandomKeepsToTheRulesForAnySource(t *testing.T) {
	chosen := selectionTenChosen()
	recorded := selectionSetOf(chosen[0], chosen[3], chosen[8])
	position := func(address string) int { return slices.Index(chosen, address) }
	for seed := uint64(0); seed < 300; seed++ {
		additions, beyondLimit := ChooseAdditions(chosen, recorded, 6, rand.New(rand.NewPCG(seed, seed+1)))
		if len(additions) != 3 || beyondLimit != 4 {
			t.Fatalf("seed %d: %d additions and %d beyond the limit, want 3 and 4", seed, len(additions), beyondLimit)
		}
		for _, address := range additions {
			if _, held := recorded[address]; held {
				t.Fatalf("seed %d: %s was picked and is already recorded", seed, address)
			}
		}
		if !slices.IsSortedFunc(additions, func(a, b string) int { return position(a) - position(b) }) {
			t.Fatalf("seed %d: additions %q are not in the site's order", seed, additions)
		}
		if len(slices.Compact(slices.Clone(additions))) != 3 {
			t.Fatalf("seed %d: additions %q repeat an address", seed, additions)
		}
	}
}

func TestChooseAdditionsAtRandomReachesEveryAddress(t *testing.T) {
	chosen := selectionSiteAddresses("/a", "/b", "/c", "/d")
	reached := map[string]bool{}
	for seed := uint64(0); seed < 300; seed++ {
		additions, _ := ChooseAdditions(chosen, nil, 1, rand.New(rand.NewPCG(seed, seed+1)))
		reached[additions[0]] = true
	}
	for _, address := range chosen {
		if !reached[address] {
			t.Errorf("%s was never picked in 300 draws of one of four", address)
		}
	}
}
