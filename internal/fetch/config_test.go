package fetch

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/store"
)

func TestRunConfigLeftBlankMeansTheDocumentedDefaults(t *testing.T) {
	got, err := RunConfig{Store: &store.Store{}}.resolved()
	if err != nil {
		t.Fatal(err)
	}
	if got.Workers != 4 || got.Rate != 2 || got.Timeout != 30*time.Second {
		t.Errorf("workers %d, rate %v, timeout %v; want 4, 2 and 30s", got.Workers, got.Rate, got.Timeout)
	}
	if got.MaxBodyBytes != 64<<20 {
		t.Errorf("body limit = %d bytes, want 64 MiB", got.MaxBodyBytes)
	}
	if got.RefusalPause != 30*time.Second {
		t.Errorf("pause for a refusal that names no time = %v, want 30s", got.RefusalPause)
	}
	if !strings.Contains(got.UserAgent, "hoard") || !strings.Contains(got.UserAgent, "https://github.com/gabrielassisxyz/hoard") {
		t.Errorf("default User-Agent %q names neither the tool nor its repository address", got.UserAgent)
	}
}

func TestRunConfigKeepsWhatItIsGiven(t *testing.T) {
	given := RunConfig{
		Store: &store.Store{}, Workers: 9, Rate: 0.5, Timeout: time.Minute, UserAgent: "mine/1",
		MaxBodyBytes: 10, RefusalPause: time.Second, Addresses: []string{"a", "b", "a"},
	}
	got, err := given.resolved()
	if err != nil {
		t.Fatal(err)
	}
	if got.Workers != 9 || got.Rate != 0.5 || got.Timeout != time.Minute || got.UserAgent != "mine/1" ||
		got.MaxBodyBytes != 10 || got.RefusalPause != time.Second {
		t.Errorf("resolved config changed what was given: %+v", got)
	}
	if len(got.Addresses) != 2 || got.Addresses[0] != "a" || got.Addresses[1] != "b" {
		t.Errorf("addresses = %q, want each kept once in first-seen order", got.Addresses)
	}
}

func TestRunConfigRefusesWhatCannotWork(t *testing.T) {
	cases := map[string]RunConfig{
		"no store":          {},
		"negative workers":  {Store: &store.Store{}, Workers: -1},
		"negative rate":     {Store: &store.Store{}, Rate: -1},
		"rate not a number": {Store: &store.Store{}, Rate: math.NaN()},
		"infinite rate":     {Store: &store.Store{}, Rate: math.Inf(1)},
		"negative timeout":  {Store: &store.Store{}, Timeout: -time.Second},
		"negative limit":    {Store: &store.Store{}, MaxBodyBytes: -1},
		"negative pause":    {Store: &store.Store{}, RefusalPause: -time.Second},
		// Reading one byte past the limit to notice an overrun would wrap around to nothing.
		"limit that overflows": {Store: &store.Store{}, MaxBodyBytes: math.MaxInt64},
	}
	for name, cfg := range cases {
		if _, err := cfg.resolved(); err == nil {
			t.Errorf("%s: resolved without an error", name)
		}
	}
}
