package fetch

import (
	"fmt"
	"slices"
	"time"
)

// ResponseTimes is how long the site took to answer each page one run stored: from just
// before the request that ended in the page was sent to the last byte of its body being read.
// Redirect hops, refusals, errors and the wait for the run's pace are not in it, so it says how
// the site was coping and not how fast the run was kept.
//
// It travels beside RunSummary and not inside it. The summary says what the ledger holds and is
// compared as a value; this is a fact about one run that the ledger does not keep.
type ResponseTimes []time.Duration

// Median is the value half the pages were under, which one slow page does not move the way it
// moves a mean. For an even number of pages it is the mean of the two middle values. It reports
// false when there is no page to take it over. The list is left in the order it was given.
func (times ResponseTimes) Median() (time.Duration, bool) {
	if len(times) == 0 {
		return 0, false
	}
	ordered := slices.Clone(times)
	slices.Sort(ordered)
	upperMiddle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[upperMiddle], true
	}
	return (ordered[upperMiddle-1] + ordered[upperMiddle]) / 2, true
}

// MedianLine is what a run says after its summary line, in whole milliseconds. The second result
// is false for a run that stored no page, which says nothing.
//
// "pages" is never made singular: a script that reads the line finds one shape.
func (times ResponseTimes) MedianLine() (string, bool) {
	median, found := times.Median()
	if !found {
		return "", false
	}
	return fmt.Sprintf("median response %d ms over %d pages", median.Round(time.Millisecond).Milliseconds(), len(times)), true
}
