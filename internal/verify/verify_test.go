package verify

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/recipe"
	"github.com/gabrielassisxyz/hoard/internal/store"
)

// capture is a real store in a temporary directory, filled through the store's own
// functions the way a fetch fills it.
type capture struct {
	t   *testing.T
	dir string
	st  *store.Store
}

func newCapture(t *testing.T) *capture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &capture{t: t, dir: dir, st: st}
}

func address(name string) string {
	return "https://site.invalid/" + name
}

func (c *capture) hold(name, body string) {
	c.t.Helper()
	if err := c.st.AddPending([]string{address(name)}); err != nil {
		c.t.Fatal(err)
	}
	ref, err := c.st.PutBody([]byte(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.st.MarkOK(address(name), store.Outcome{HTTPStatus: 200, FetchedAt: time.Now(), Body: ref}); err != nil {
		c.t.Fatal(err)
	}
}

func (c *capture) fail(name string) {
	c.t.Helper()
	if err := c.st.AddPending([]string{address(name)}); err != nil {
		c.t.Fatal(err)
	}
	if err := c.st.MarkFailed(address(name), store.Outcome{HTTPStatus: 404, Err: "HTTP 404 Not Found"}); err != nil {
		c.t.Fatal(err)
	}
}

func (c *capture) leavePending(name string) {
	c.t.Helper()
	if err := c.st.AddPending([]string{address(name)}); err != nil {
		c.t.Fatal(err)
	}
}

func (c *capture) check(criterion recipe.Verify) Report {
	c.t.Helper()
	report, err := Check(c.st, criterion)
	if err != nil {
		c.t.Fatalf("Check: %v", err)
	}
	return report
}

func printed(report Report) string {
	var out bytes.Buffer
	report.Print(&out)
	return out.String()
}

// articleOf is a body of exactly size bytes that contains no forbidden text.
func articleOf(size int) string {
	return strings.Repeat("a", size)
}

func TestACaptureThatMeetsEveryPartOfTheCriterionVerifies(t *testing.T) {
	c := newCapture(t)
	c.hold("one", articleOf(40))
	c.hold("two", articleOf(41))
	c.hold("three", articleOf(400))
	report := c.check(recipe.Verify{MinPages: 3, MinBodyBytes: 40, Forbidden: []string{"Subscribe to read"}})
	if !report.Met() {
		t.Fatalf("the criterion is not met: %+v", report)
	}
	if got := printed(report); got != "verify: ok, 3 pages held\n" {
		t.Errorf("printed %q", got)
	}
}

func TestTooFewPagesBreaksTheCriterion(t *testing.T) {
	c := newCapture(t)
	c.hold("one", articleOf(40))
	c.hold("two", articleOf(40))
	report := c.check(recipe.Verify{MinPages: 3})
	if report.Met() || !report.TooFewPages() {
		t.Fatalf("2 pages verified against a minimum of 3: %+v", report)
	}
	if got := printed(report); !strings.Contains(got, "verify: not met") || !strings.Contains(got, "pages held: 2, at least 3 required") {
		t.Errorf("printed:\n%s", got)
	}
	if atTheMinimum := c.check(recipe.Verify{MinPages: 2}); !atTheMinimum.Met() {
		t.Errorf("2 pages did not verify against a minimum of 2: %+v", atTheMinimum)
	}
}

func TestABodyUnderTheMinimumSizeBreaksTheCriterion(t *testing.T) {
	c := newCapture(t)
	c.hold("exactly", articleOf(100))
	c.hold("one-short", articleOf(99))
	c.hold("long", articleOf(5000))
	report := c.check(recipe.Verify{MinPages: 1, MinBodyBytes: 100})
	if report.Met() {
		t.Fatalf("a 99-byte body verified against a minimum of 100: %+v", report)
	}
	if strings.Join(report.TooSmall, " ") != address("one-short") {
		t.Errorf("TooSmall = %v, want only the 99-byte page; exactly the minimum passes", report.TooSmall)
	}
	if got := printed(report); !strings.Contains(got, "smaller than 100 bytes: 1 pages") || !strings.Contains(got, address("one-short")) {
		t.Errorf("printed:\n%s", got)
	}
}

func TestABodyHoldingAForbiddenTextBreaksTheCriterion(t *testing.T) {
	c := newCapture(t)
	c.hold("article", "<p>The whole article, with the words subscribe to read in lower case.</p>")
	c.hold("teaser", "<p>First paragraph.</p><div class=\"wall\">Subscribe to read</div>")
	report := c.check(recipe.Verify{MinPages: 1, Forbidden: []string{"Subscribe to read", "never-present"}})
	if report.Met() {
		t.Fatalf("a page holding a forbidden text verified: %+v", report)
	}
	if len(report.Forbidden) != 1 || report.Forbidden[0].Text != "Subscribe to read" {
		t.Fatalf("Forbidden = %+v, want one finding, for the text that was found", report.Forbidden)
	}
	if strings.Join(report.Forbidden[0].Addresses, " ") != address("teaser") {
		t.Errorf("found in %v, want only the teaser; the match is case-sensitive", report.Forbidden[0].Addresses)
	}
	if got := printed(report); !strings.Contains(got, `containing "Subscribe to read": 1 pages`) || !strings.Contains(got, address("teaser")) {
		t.Errorf("printed:\n%s", got)
	}
}

func TestABrokenPartNamesItsCountAndItsFirstTenPages(t *testing.T) {
	c := newCapture(t)
	for i := 0; i < 25; i++ {
		c.hold(fmt.Sprintf("small-%02d", i), articleOf(5+i))
	}
	c.hold("large", articleOf(900))
	report := c.check(recipe.Verify{MinPages: 1, MinBodyBytes: 500})
	if len(report.TooSmall) != 25 {
		t.Fatalf("TooSmall has %d pages, want 25", len(report.TooSmall))
	}
	got := printed(report)
	if !strings.Contains(got, "smaller than 500 bytes: 25 pages") {
		t.Errorf("the count is not 25:\n%s", got)
	}
	if listed := strings.Count(got, "https://site.invalid/small-"); listed != 10 {
		t.Errorf("%d addresses are listed, want 10:\n%s", listed, got)
	}
	if !strings.Contains(got, address("small-00")) || strings.Contains(got, address("small-10")) {
		t.Errorf("the 10 listed are not the first 10 in address order:\n%s", got)
	}
	if !strings.Contains(got, "and 15 more") {
		t.Errorf("the report does not say how many it left out:\n%s", got)
	}
}

func TestFailedAndPendingAddressesAreReportedAndNotJudged(t *testing.T) {
	c := newCapture(t)
	c.hold("one", articleOf(200))
	c.hold("two", articleOf(200))
	c.fail("dead-link")
	c.leavePending("not-yet")
	// A failed or pending row has no body: judged by size it would break any minimum.
	report := c.check(recipe.Verify{MinPages: 2, MinBodyBytes: 100, Forbidden: []string{"Subscribe to read"}})
	if !report.Met() {
		t.Fatalf("a failed and a pending address broke the criterion: %+v", report)
	}
	if report.Held != 2 || report.Failed != 1 || report.Pending != 1 {
		t.Errorf("counts = %d held, %d failed, %d pending; want 2, 1, 1", report.Held, report.Failed, report.Pending)
	}
	if got := printed(report); got != "verify: ok, 2 pages held (1 failed, 1 pending)\n" {
		t.Errorf("printed %q", got)
	}
	notMet := c.check(recipe.Verify{MinPages: 3})
	if got := printed(notMet); !strings.Contains(got, "not part of the criterion: 1 failed, 1 pending") {
		t.Errorf("the unmet report leaves out the unsettled addresses:\n%s", got)
	}
}

func TestBodiesAreReadOnlyWhenATextIsForbidden(t *testing.T) {
	c := newCapture(t)
	c.hold("one", articleOf(200))
	// With the bodies gone, only a check that reads them can notice.
	if err := os.RemoveAll(filepath.Join(c.dir, "raw")); err != nil {
		t.Fatal(err)
	}
	if report, err := Check(c.st, recipe.Verify{MinPages: 1, MinBodyBytes: 100}); err != nil || !report.Met() {
		t.Errorf("a criterion of counts and sizes needed the bodies: report %+v, err %v", report, err)
	}
	if _, err := Check(c.st, recipe.Verify{MinPages: 1, Forbidden: []string{"x"}}); err == nil {
		t.Error("a forbidden text was checked without the bodies and no error came back")
	}
}
