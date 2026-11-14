package fetch

import (
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/store"
)

func TestParseListKeepsAddressesAndSkipsBlankLinesAndComments(t *testing.T) {
	text := "# the pages to bring home\n" +
		"https://site.test/1\n" +
		"\n" +
		"   https://site.test/2   \n" +
		"\t# an indented comment\n" +
		"https://site.test/3#fragment-is-not-a-comment\r\n" +
		"   \r\n" +
		"not even an address\n" +
		"https://site.test/1"
	want := []string{
		"https://site.test/1",
		"https://site.test/2",
		"https://site.test/3#fragment-is-not-a-comment",
		"not even an address",
		"https://site.test/1",
	}
	got, err := ParseList(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseList =\n %q\nwant\n %q", got, want)
	}
}

func TestParseListOfNothingIsEmpty(t *testing.T) {
	for _, text := range []string{"", "\n\n", "# only a comment"} {
		got, err := ParseList(strings.NewReader(text))
		if err != nil || len(got) != 0 {
			t.Errorf("ParseList(%q) = %q, %v", text, got, err)
		}
	}
}

func TestParseListReportsALineTooLongToRead(t *testing.T) {
	if _, err := ParseList(strings.NewReader(strings.Repeat("a", maxListLineBytes+1))); err == nil {
		t.Fatal("a line over the limit was accepted")
	}
}

func TestParseRetryAfterReadsSecondsAndHTTPDates(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		header string
		want   time.Duration
		ok     bool
	}{
		{"120", 120 * time.Second, true},
		{"  7  ", 7 * time.Second, true},
		{"0", 0, true},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		{"Sunday, 15-Nov-26 12:01:30 GMT", 90*time.Second + 24*time.Hour, true}, // RFC 850 form
		{"Sun Nov 15 12:00:30 2026", 24*time.Hour + 30*time.Second, true},       // asctime form
		{"99999999999999999999999999", time.Duration(math.MaxInt64), true},
		{"9223372036854775807", time.Duration(math.MaxInt64), true},
		{"", 0, false},
		{"soon", 0, false},
		{"-5", 0, false},
		{"1.5", 0, false},
		{"12 seconds", 0, false},
		{"Tue, 99 Foo 2026 25:61:61 GMT", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseRetryAfter(c.header, now)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseRetryAfter(%q) = %v, %v; want %v, %v", c.header, got, ok, c.want, c.ok)
		}
	}
}

func TestRefusalPauseDoublesPerRefusalUpToTheCap(t *testing.T) {
	cases := []struct {
		base   time.Duration
		streak int
		want   time.Duration
	}{
		{30 * time.Second, 1, 30 * time.Second},
		{30 * time.Second, 2, time.Minute},
		{30 * time.Second, 3, 2 * time.Minute},
		{30 * time.Second, 4, 4 * time.Minute},
		{30 * time.Second, 5, 8 * time.Minute},
		{30 * time.Second, 6, 10 * time.Minute},
		{30 * time.Second, 7, 10 * time.Minute},
		{30 * time.Second, 100000, 10 * time.Minute},
		{time.Second, 1, time.Second},
		{time.Second, 2, 2 * time.Second},
		{time.Second, 3, 4 * time.Second},
		{0, 9, 0},
		{time.Hour, 1, 10 * time.Minute},
		{time.Duration(math.MaxInt64), 3, 10 * time.Minute},
	}
	for _, c := range cases {
		if got := refusalPause(c.base, c.streak); got != c.want {
			t.Errorf("refusalPause(%v, %d) = %v, want %v", c.base, c.streak, got, c.want)
		}
	}
}

func TestHostKeyTreatsSpellingsOfOneSiteAsOne(t *testing.T) {
	same := [][]string{
		{"http://Example.com/a", "http://example.com:80/b", "HTTP://EXAMPLE.COM", "https://example.com/c", "https://example.com:443/d"},
		{"http://example.com:8080/x", "https://EXAMPLE.com:8080/y"},
		{"http://[::1]:8080/x", "http://[::1]:8080/y"},
		{"http://[::1]/x", "https://[::1]:443/y"},
	}
	for _, group := range same {
		first := mustHostKey(t, group[0])
		for _, spelling := range group[1:] {
			if got := mustHostKey(t, spelling); got != first {
				t.Errorf("hostKey(%q) = %q, but hostKey(%q) = %q; they are one site", spelling, got, group[0], first)
			}
		}
	}
	distinct := []string{
		"http://example.com/", "http://example.com:8080/", "http://www.example.com/",
		"http://[::1]:8080/", "http://[::1]/", "http://other.test/",
	}
	seen := map[string]string{}
	for _, address := range distinct {
		key := mustHostKey(t, address)
		if previous, dup := seen[key]; dup {
			t.Errorf("%q and %q share the key %q but are different sites", previous, address, key)
		}
		seen[key] = address
	}
}

func TestEachHostGetsALimiterAtTheStatedRateWithABurstOfOne(t *testing.T) {
	for _, rate := range []float64{0.5, 2, 50} {
		limiter := newHostQueue(rate).limiter
		if got := float64(limiter.Limit()); got != rate {
			t.Errorf("limiter rate = %v, want %v", got, rate)
		}
		// Arrival times cannot show this reliably: a burst of two at 50 a second puts the extra
		// request just past the edge of any one-second window, and the gap between the pair is
		// lost in scheduling jitter. The limiter's own setting is exact.
		if got := limiter.Burst(); got != 1 {
			t.Errorf("limiter burst at %v a second = %d, want 1", rate, got)
		}
	}
}

func schedulerForUnitTest(t *testing.T) *fetchScheduler {
	t.Helper()
	cfg, err := RunConfig{Store: &store.Store{}}.resolved()
	if err != nil {
		t.Fatal(err)
	}
	return newFetchScheduler(cfg)
}

func TestHostsGetTheirOwnQueueAndLimiterAndSpellingsOfOneShareIt(t *testing.T) {
	scheduler := schedulerForUnitTest(t)
	parse := func(address string) *url.URL {
		parsed, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	one, two := scheduler.hostFor(parse("http://one.test/x")), scheduler.hostFor(parse("http://two.test/x"))
	if one == two {
		t.Error("two hosts share a queue and with it a pause")
	}
	if one.limiter == two.limiter {
		t.Error("two hosts share a limiter, so one pace is divided between them")
	}
	if again := scheduler.hostFor(parse("HTTP://ONE.test:80/y")); again != one {
		t.Error("two spellings of one host got two queues, which doubles the pace it is asked at")
	}
}

func TestRefusalsAreInARowOnlyWhenSentAfterThePreviousOne(t *testing.T) {
	scheduler := schedulerForUnitTest(t)
	host := newHostQueue(1000)
	refused := attemptResult{kind: attemptRefused, retryAfter: time.Second, hasRetryAfter: true}
	answered := attemptResult{kind: attemptStored, outcome: store.Outcome{HTTPStatus: 200}}
	pause := func() { time.Sleep(2 * time.Millisecond) }
	job := func(sentAt time.Time) *pageJob { return &pageJob{host: host, startedAt: sentAt} }

	sentTogether := time.Now()
	pause()
	for range 4 {
		scheduler.learnFromHost(job(sentTogether), refused)
	}
	if host.refusals != 1 {
		t.Fatalf("four requests sent together and refused together made a streak of %d, want 1", host.refusals)
	}

	pause()
	sentAfter := time.Now()
	pause()
	scheduler.learnFromHost(job(sentAfter), refused)
	if host.refusals != 2 {
		t.Fatalf("a request sent after the refusal and refused again made a streak of %d, want 2", host.refusals)
	}

	pause()
	scheduler.learnFromHost(job(sentTogether), answered)
	if host.refusals != 2 {
		t.Fatalf("an answer to a request sent before the last refusal changed the streak to %d, want it left at 2", host.refusals)
	}

	pause()
	sentLater := time.Now()
	pause()
	scheduler.learnFromHost(job(sentLater), answered)
	if host.refusals != 0 {
		t.Fatalf("an answer to a request sent after the last refusal left the streak at %d, want 0", host.refusals)
	}
	if !host.pausedUntil.After(time.Now()) {
		t.Error("the refusals did not rest the host")
	}
}

func mustHostKey(t *testing.T, address string) string {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return hostKey(parsed)
}
