package discover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"golang.org/x/time/rate"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/hostkey"
	"github.com/gabrielassisxyz/hoard/internal/recipe"
)

const (
	// The sitemap protocol's own ceiling is 50 MB uncompressed; this leaves room above it.
	maxDocumentBytes = 64 << 20
	// The same as the standard client's own cap.
	maxRedirects = 10
)

// siteClient asks the site for the few documents a discovery reads. They are not pages, so
// they do not go through the ledger, but they are still requests to the site: paced at the
// recipe's rate and carrying its user agent.
type siteClient struct {
	http      *http.Client
	limiter   *rate.Limiter
	userAgent string
	cookie    string
	siteHost  string
}

func newSiteClient(r recipe.Recipe) (*siteClient, error) {
	cookie, err := r.CookieHeader()
	if err != nil {
		return nil, err
	}
	requestsPerSecond := r.Fetch.Rate
	if requestsPerSecond == 0 {
		requestsPerSecond = fetch.DefaultRate
	}
	timeout := r.Fetch.Timeout
	if timeout == 0 {
		timeout = fetch.DefaultTimeout
	}
	userAgent := r.Fetch.UserAgent
	if userAgent == "" {
		userAgent = fetch.DefaultUserAgent
	}
	return &siteClient{
		http: &http.Client{
			Timeout: timeout,
			// Each hop is built here, so that the cookie is decided per request and never
			// carried along by the client.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		limiter:   rate.NewLimiter(rate.Limit(requestsPerSecond), 1),
		userAgent: userAgent,
		cookie:    cookie,
		siteHost:  r.Site.Host,
	}, nil
}

func (c *siteClient) close() {
	c.http.CloseIdleConnections()
}

// get returns the document at target and the address it was finally served from. Anything
// but a 2xx at the end of the redirects is an error: a discovery reads every document it
// needs or it has no list.
func (c *siteClient) get(ctx context.Context, target *url.URL) ([]byte, *url.URL, error) {
	asked := target.String()
	for hop := 0; hop <= maxRedirects; hop++ {
		body, next, err := c.getOnce(ctx, target)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", asked, err)
		}
		if next == nil {
			return body, target, nil
		}
		target = next
	}
	return nil, nil, fmt.Errorf("%s: more than %d redirects", asked, maxRedirects)
}

// getOnce makes one request. It returns the body of a 2xx, or the address a redirect
// points to.
func (c *siteClient) getOnce(ctx context.Context, target *url.URL) ([]byte, *url.URL, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	if c.cookie != "" && hostkey.SessionMayGoTo(c.siteHost, target) {
		req.Header.Set("Cookie", c.cookie)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if location, err := resp.Location(); err == nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, location, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, errors.New("HTTP " + resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxDocumentBytes {
		return nil, nil, fmt.Errorf("document is larger than %d bytes", maxDocumentBytes)
	}
	return body, nil, nil
}
