package fetch_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

// The tests that wait for a site assert bounds and never a figure: a loaded machine makes a
// request slower, never faster than the site's own wait, and an upper bound is only ever set far
// from the value that the rule it guards would give.

func TestMedianIsTheMiddleValueForAnOddCount(t *testing.T) {
	cases := []struct {
		name  string
		times fetch.ResponseTimes
		want  time.Duration
	}{
		{"one page", fetch.ResponseTimes{7 * time.Millisecond}, 7 * time.Millisecond},
		{"three pages, in no order", fetch.ResponseTimes{30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}, 20 * time.Millisecond},
		{"five pages, one of them far slower than the rest",
			fetch.ResponseTimes{12 * time.Millisecond, 9 * time.Millisecond, 5 * time.Second, 11 * time.Millisecond, 10 * time.Millisecond},
			11 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found := c.times.Median()
			if !found || got != c.want {
				t.Errorf("Median() = %v, %v; want %v, true", got, found, c.want)
			}
		})
	}
}

func TestMedianIsTheMeanOfTheTwoMiddleValuesForAnEvenCount(t *testing.T) {
	cases := []struct {
		name  string
		times fetch.ResponseTimes
		want  time.Duration
	}{
		{"two pages", fetch.ResponseTimes{30 * time.Millisecond, 10 * time.Millisecond}, 20 * time.Millisecond},
		{"four pages, in no order", fetch.ResponseTimes{40 * time.Millisecond, 10 * time.Millisecond, 30 * time.Millisecond, 20 * time.Millisecond}, 25 * time.Millisecond},
		{"a mean that is not a whole millisecond", fetch.ResponseTimes{2 * time.Millisecond, time.Millisecond}, 1500 * time.Microsecond},
		{"four pages, one of them far slower than the rest",
			fetch.ResponseTimes{12 * time.Millisecond, 5 * time.Second, 11 * time.Millisecond, 10 * time.Millisecond},
			11500 * time.Microsecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found := c.times.Median()
			if !found || got != c.want {
				t.Errorf("Median() = %v, %v; want %v, true", got, found, c.want)
			}
		})
	}
}

func TestMedianReportsThatThereIsNoneForAnEmptyList(t *testing.T) {
	for name, times := range map[string]fetch.ResponseTimes{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			if got, found := times.Median(); found || got != 0 {
				t.Errorf("Median() = %v, %v; want 0, false", got, found)
			}
		})
	}
}

func TestMedianLeavesTheListInTheOrderItWasGiven(t *testing.T) {
	times := fetch.ResponseTimes{30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}
	given := slices.Clone(times)

	times.Median()

	if !slices.Equal(times, given) {
		t.Errorf("the list is now %v, it was given as %v", times, given)
	}
}

func TestMedianLineSaysWholeMillisecondsAndHowManyPages(t *testing.T) {
	cases := []struct {
		name  string
		times fetch.ResponseTimes
		want  string
	}{
		{"three pages", fetch.ResponseTimes{80 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond}, "median response 80 ms over 3 pages"},
		{"one page is still pages, for a script that reads one shape", fetch.ResponseTimes{40 * time.Millisecond}, "median response 40 ms over 1 pages"},
		{"under half a millisecond rounds down", fetch.ResponseTimes{1400 * time.Microsecond}, "median response 1 ms over 1 pages"},
		{"over half a millisecond rounds up", fetch.ResponseTimes{1600 * time.Microsecond}, "median response 2 ms over 1 pages"},
		{"a page answered at once is still a number", fetch.ResponseTimes{200 * time.Microsecond}, "median response 0 ms over 1 pages"},
		{"an even count counts every page, not the two in the middle", fetch.ResponseTimes{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond}, "median response 25 ms over 4 pages"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, said := c.times.MedianLine(); !said || got != c.want {
				t.Errorf("MedianLine() = %q, %v; want %q, true", got, said, c.want)
			}
		})
	}
}

func TestMedianLineSaysNothingForARunThatStoredNoPage(t *testing.T) {
	if got, said := fetch.ResponseTimes(nil).MedianLine(); said || got != "" {
		t.Errorf("MedianLine() = %q, %v; want nothing", got, said)
	}
}

// runFetchForResponseTimes is runFetchOver for a test that needs what the run says about how
// long the site took, with the same pace: fast enough that the limiter is not what a test is
// measuring unless it says so.
func runFetchForResponseTimes(t *testing.T, st *store.Store, addresses []string, tweaks ...func(*fetch.RunConfig)) (fetch.RunSummary, fetch.ResponseTimes) {
	t.Helper()
	cfg := fetch.RunConfig{Store: st, Addresses: addresses, Workers: 4, Rate: 1000, Timeout: 5 * time.Second}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	summary, times, err := fetch.RunWithResponseTimes(ctx, cfg)
	if err != nil {
		t.Fatalf("fetch.RunWithResponseTimes: %v", err)
	}
	return summary, times
}

// siteAnsweringAfter is a synthetic site whose every page takes wait to be answered.
func siteAnsweringAfter(t *testing.T, wait time.Duration) *recordingSite {
	t.Helper()
	return newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(wait)
		writeSyntheticHTML(w, "<p>"+r.URL.Path+"</p>")
	})
}

func TestRunTimesEachStoredPageByTheWaitOfTheSite(t *testing.T) {
	const wait = 80 * time.Millisecond
	site := siteAnsweringAfter(t, wait)
	st, _ := openFreshStore(t)

	summary, times := runFetchForResponseTimes(t, st, numberedAddresses(site, "w", 3))

	if !summary.Complete() {
		t.Fatalf("summary = %v", summary)
	}
	if len(times) != 3 {
		t.Fatalf("%d response times for 3 stored pages", len(times))
	}
	for i, took := range times {
		if took < wait {
			t.Errorf("page %d took %v to answer, and the site waited %v before it did", i, took, wait)
		}
	}
	if median, _ := times.Median(); median < wait {
		t.Errorf("the median is %v, and the site waited %v before every answer", median, wait)
	}
}

func TestRunTimesAPageUntilTheLastByteOfItsBody(t *testing.T) {
	const wait = 80 * time.Millisecond
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		// The headers are on their way at once and the body is not: a clock that stops when the
		// answer begins stops before the wait.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(wait)
		w.Write([]byte("<p>the body comes late</p>"))
	})
	st, _ := openFreshStore(t)

	summary, times := runFetchForResponseTimes(t, st, numberedAddresses(site, "b", 2))

	if !summary.Complete() || len(times) != 2 {
		t.Fatalf("summary = %v with %d response times", summary, len(times))
	}
	for i, took := range times {
		if took < wait {
			t.Errorf("page %d took %v, and the last byte of its body came %v after the headers", i, took, wait)
		}
	}
}

func TestRunDoesNotCountTheWaitForThePaceInAResponseTime(t *testing.T) {
	site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
		writeSyntheticHTML(w, "<p>at once</p>")
	})
	st, _ := openFreshStore(t)

	summary, times := runFetchForResponseTimes(t, st, numberedAddresses(site, "q", 3), func(cfg *fetch.RunConfig) { cfg.Rate = 2 })

	if !summary.Complete() || len(times) != 3 {
		t.Fatalf("summary = %v with %d response times", summary, len(times))
	}
	// Not vacuous: at 2 a second the pace holds the second page back for half a second and the
	// third for a whole one, so a clock that ran while a request waited for it would read that.
	arrivals := site.arrivalTimes("")
	if held := arrivals[len(arrivals)-1].Sub(arrivals[0]); held < 900*time.Millisecond {
		t.Fatalf("the pace let three requests through within %v, want a second between the first and the last", held)
	}
	if median, _ := times.Median(); median >= 400*time.Millisecond {
		t.Errorf("the median is %v for a site that answers at once; the wait for the pace was counted", median)
	}
}

func TestRunTimesOnlyTheRequestThatEndedInTheStoredPage(t *testing.T) {
	// What comes before the page takes longer than the page ever could, so that counting it
	// puts the page over the bound however loaded the machine is.
	const slowRequest = 800 * time.Millisecond
	const bound = 500 * time.Millisecond

	t.Run("a redirect's earlier hop", func(t *testing.T) {
		t.Parallel()
		site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/old" {
				time.Sleep(slowRequest)
				http.Redirect(w, r, "/new", http.StatusFound)
				return
			}
			writeSyntheticHTML(w, "<p>the page it led to</p>")
		})
		st, _ := openFreshStore(t)

		summary, times := runFetchForResponseTimes(t, st, site.addresses("/old"))

		if !summary.Complete() || len(times) != 1 || len(site.arrivalTimes("/new")) != 1 {
			t.Fatalf("summary = %v with %d response times; the redirect was not followed once", summary, len(times))
		}
		if times[0] >= bound {
			t.Errorf("the page took %v, and its request for the final address was answered at once: the hop before it was counted", times[0])
		}
	})

	t.Run("a failed attempt before it", func(t *testing.T) {
		t.Parallel()
		failing := &failuresThenSuccess{n: 1}
		site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
			if failing.stillFailing() {
				time.Sleep(slowRequest)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			writeSyntheticHTML(w, "<p>the second try</p>")
		})
		st, _ := openFreshStore(t)

		summary, times := runFetchForResponseTimes(t, st, site.addresses("/flaky"))

		if !summary.Complete() || len(times) != 1 || site.requestCount() != 2 {
			t.Fatalf("summary = %v with %d response times after %d requests; want one retry", summary, len(times), site.requestCount())
		}
		if times[0] >= bound {
			t.Errorf("the page took %v, and its second request was answered at once: the failed one was counted", times[0])
		}
	})
}

func TestRunHasNoResponseTimeForARunThatStoredNoPage(t *testing.T) {
	t.Run("a second run over a finished capture", func(t *testing.T) {
		site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
			writeSyntheticHTML(w, "<p>held</p>")
		})
		st, _ := openFreshStore(t)
		addresses := numberedAddresses(site, "s", 3)

		_, first := runFetchForResponseTimes(t, st, addresses)
		summary, second := runFetchForResponseTimes(t, st, addresses)

		if len(first) != 3 {
			t.Fatalf("the first run has %d response times for 3 stored pages", len(first))
		}
		if !summary.Complete() || site.requestCount() != 3 {
			t.Fatalf("summary = %v after %d requests; the second run was meant to ask for nothing", summary, site.requestCount())
		}
		if len(second) != 0 {
			t.Errorf("the second run, which stored nothing, has %d response times", len(second))
		}
	})

	t.Run("every address fails", func(t *testing.T) {
		site := newRecordingSite(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/broken" {
				http.Error(w, "broken", http.StatusInternalServerError)
				return
			}
			http.NotFound(w, r)
		})
		st, _ := openFreshStore(t)

		summary, times := runFetchForResponseTimes(t, st, site.addresses("/missing", "/gone", "/broken"))

		if summary.Failed != 3 || summary.Held != 0 {
			t.Fatalf("summary = %v; want all three addresses failed", summary)
		}
		if len(times) != 0 {
			t.Errorf("a run that stored no page has %d response times", len(times))
		}
	})
}
