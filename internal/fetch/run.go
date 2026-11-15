package fetch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/store"
)

// The defaults the command line shows. A zero field in RunConfig means the same default.
const (
	DefaultWorkers = 4
	DefaultRate    = 2.0 // requests per second over the whole run, whatever host each goes to
	DefaultTimeout = 30 * time.Second
	// A current desktop browser's. Sites answer a browser; some answer anything else
	// differently or not at all, and a capture is meant to hold what a reader sees.
	DefaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36"

	// Per address, per run, whatever each attempt turned out to be.
	maxAttemptsPerRun   = 3
	defaultMaxBodyBytes = 64 << 20
	// The same as the standard client's own cap, so a redirect loop ends in the same place.
	maxRedirectsPerAddress = 10
	// Far beyond any page, and low enough that reading one byte past it cannot overflow.
	largestBodyLimit = 1 << 40
)

// RunConfig is everything Run needs. Only Store and Addresses are required.
type RunConfig struct {
	Store     *store.Store
	Addresses []string

	// Workers is the most requests in flight at once across all hosts.
	Workers int
	// Rate is requests per second over the whole run, to every host together, with no burst
	// beyond one.
	Rate      float64
	Timeout   time.Duration
	UserAgent string

	// Cookie is the value of the Cookie header for the one site named by CookieHost. Empty
	// means the run is signed in nowhere.
	Cookie string
	// CookieHost is that site's key, as hostkey.Of gives it. The cookie goes to requests
	// hostkey.SessionMayGoTo allows for it and to no other.
	CookieHost string

	// Limits the command line does not offer. They are fields so a test can exercise them
	// without waiting out the real values.
	MaxBodyBytes int64
	// RefusalPause is how long a host rests after a refusal that names no time of its own.
	RefusalPause time.Duration
}

// RunSummary says what the run left in the ledger for the addresses it was given.
type RunSummary struct {
	Listed  int // distinct addresses
	Held    int // ok: the body is in the store
	Failed  int // failed, in this run or an earlier one
	Pending int // not settled: the run was interrupted or the store gave out
}

// Complete is true only when every listed address is held.
func (s RunSummary) Complete() bool {
	return s.Held == s.Listed
}

func (s RunSummary) String() string {
	return fmt.Sprintf("%d listed, %d held, %d failed, %d pending", s.Listed, s.Held, s.Failed, s.Pending)
}

// Run fetches the addresses that the ledger does not already settle. An address that is
// ok or failed keeps its row and is not requested again; one that is pending, whether new
// or left over from an interrupted run, is fetched.
//
// The summary describes the ledger after the run, even when Run also returns an error.
// Cancelling ctx stops the run cleanly: nothing new is started, what was in flight is
// abandoned, and the addresses not yet settled stay pending for the next run.
func Run(ctx context.Context, cfg RunConfig) (RunSummary, error) {
	summary, _, err := RunWithResponseTimes(ctx, cfg)
	return summary, err
}

// RunWithResponseTimes is Run, and also says how long the site took to answer each page this
// run stored. A run that stored nothing, such as a second one over a finished capture, has no
// times to give: nothing in the ledger records them.
func RunWithResponseTimes(ctx context.Context, cfg RunConfig) (RunSummary, ResponseTimes, error) {
	cfg, err := cfg.resolved()
	if err != nil {
		return RunSummary{}, nil, err
	}
	if err := cfg.Store.AddPending(cfg.Addresses); err != nil {
		return RunSummary{}, nil, err
	}
	eng := newFetchScheduler(cfg)
	defer eng.client.CloseIdleConnections()
	if err := eng.enqueuePending(); err != nil {
		return RunSummary{}, nil, err
	}
	loopErr := eng.loop(ctx)
	summary, summaryErr := eng.summarize()
	return summary, eng.responseTimes, errors.Join(loopErr, summaryErr)
}

func (c RunConfig) resolved() (RunConfig, error) {
	if c.Store == nil {
		return c, errors.New("fetch: no store to fetch into")
	}
	if c.Workers < 0 || c.Timeout < 0 || c.MaxBodyBytes < 0 || c.RefusalPause < 0 ||
		c.Rate < 0 || math.IsNaN(c.Rate) || math.IsInf(c.Rate, 0) {
		return c, errors.New("fetch: workers, rate, timeout and limits cannot be negative")
	}
	if c.Cookie != "" && c.CookieHost == "" {
		return c, errors.New("fetch: a cookie was given without the host it belongs to")
	}
	if c.MaxBodyBytes > largestBodyLimit {
		return c, fmt.Errorf("fetch: a body limit of %d bytes is above the %d that can be enforced", c.MaxBodyBytes, int64(largestBodyLimit))
	}
	c.Addresses = listedOnce(c.Addresses)
	c.Workers = zeroToDefault(c.Workers, DefaultWorkers)
	c.Rate = zeroToDefault(c.Rate, DefaultRate)
	c.Timeout = zeroToDefault(c.Timeout, DefaultTimeout)
	c.UserAgent = zeroToDefault(c.UserAgent, DefaultUserAgent)
	c.MaxBodyBytes = zeroToDefault(c.MaxBodyBytes, defaultMaxBodyBytes)
	c.RefusalPause = zeroToDefault(c.RefusalPause, defaultRefusalPause)
	return c, nil
}

func zeroToDefault[T comparable](value, fallback T) T {
	var zero T
	if value == zero {
		return fallback
	}
	return value
}

// listedOnce keeps the first occurrence of each address, so that "every listed address has
// exactly one row" and "is requested once" hold however the list was assembled.
func listedOnce(addresses []string) []string {
	seen := make(map[string]struct{}, len(addresses))
	kept := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if _, dup := seen[address]; dup {
			continue
		}
		seen[address] = struct{}{}
		kept = append(kept, address)
	}
	return kept
}

// enqueuePending turns every address the ledger leaves open into a job on its host's
// queue, and settles at once the ones that cannot be requested at all.
func (e *fetchScheduler) enqueuePending() error {
	for _, address := range e.cfg.Addresses {
		page, found, err := e.cfg.Store.Page(address)
		if err != nil {
			return err
		}
		if !found || page.State != store.StatePending {
			continue
		}
		parsed, problem := parseFetchableAddress(address)
		if problem != "" {
			if err := e.cfg.Store.MarkRejected(address, problem); err != nil {
				return err
			}
			continue
		}
		host := e.hostFor(parsed)
		host.waiting = append(host.waiting, &pageJob{address: address, target: parsed, host: host})
		e.remaining++
	}
	return nil
}

func parseFetchableAddress(address string) (*url.URL, string) {
	parsed, err := url.Parse(address)
	if err != nil {
		return nil, "invalid address: " + err.Error()
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, "invalid address: only absolute http and https addresses can be fetched"
	}
	return parsed, ""
}

func (e *fetchScheduler) hostFor(parsed *url.URL) *hostQueue {
	key := hostKey(parsed)
	if host, ok := e.hostByKey[key]; ok {
		return host
	}
	host := &hostQueue{}
	e.hostByKey[key] = host
	e.hosts = append(e.hosts, host)
	return host
}

func (e *fetchScheduler) summarize() (RunSummary, error) {
	summary := RunSummary{Listed: len(e.cfg.Addresses)}
	for _, address := range e.cfg.Addresses {
		page, found, err := e.cfg.Store.Page(address)
		if err != nil {
			return summary, err
		}
		switch {
		case found && page.State == store.StateOK:
			summary.Held++
		case found && page.State == store.StateFailed:
			summary.Failed++
		default:
			summary.Pending++
		}
	}
	return summary, nil
}
