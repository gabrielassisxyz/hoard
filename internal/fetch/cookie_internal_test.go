package fetch

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

// recordingTransport answers every request with a 404 and keeps the request, so a test can
// read the headers of a request to an address no local server could stand in for: a real
// host name, https, or plain http to a machine that is not this one.
type recordingTransport struct {
	requests []*http.Request
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, req)
	return &http.Response{
		Status:     "404 Not Found",
		StatusCode: http.StatusNotFound,
		Header:     http.Header{},
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

// headersSentTo makes the one request a job for address would make and returns its headers.
func headersSentTo(t *testing.T, cfg RunConfig, address string) http.Header {
	t.Helper()
	target, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := newFetchScheduler(cfg)
	transport := &recordingTransport{}
	scheduler.client.Transport = transport
	scheduler.attempt(context.Background(), &pageJob{address: address, target: target})
	if len(transport.requests) != 1 {
		t.Fatalf("attempt on %s made %d requests, want 1", address, len(transport.requests))
	}
	return transport.requests[0].Header
}

func TestAttemptSendsTheCookieOnlyWhereTheSessionMayGo(t *testing.T) {
	const cookie = "session=belongs-to-example"
	cfg := RunConfig{UserAgent: "test", Workers: 1, Cookie: cookie, CookieHost: "example.com"}
	cases := []struct {
		name    string
		address string
		signed  bool
	}{
		{"the site over https", "https://example.com/article", true},
		{"the site, spelt with its default port", "https://EXAMPLE.com:443/article", true},
		{"the site over plain http", "http://example.com/article", false},
		{"a subdomain of the site", "https://cdn.example.com/article", false},
		{"another host", "https://tracker.invalid/pixel", false},
		{"the site on another port", "https://example.com:8443/article", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sent := headersSentTo(t, cfg, c.address)
			values, has := sent["Cookie"]
			switch {
			case c.signed && (!has || len(values) != 1 || values[0] != cookie):
				t.Errorf("request to %s carried Cookie %q, want exactly the configured one", c.address, values)
			case !c.signed && has:
				t.Errorf("request to %s carried a Cookie header; the session may not go there", c.address)
			}
		})
	}
}

func TestAttemptSendsNoCookieWhenNoneIsConfigured(t *testing.T) {
	sent := headersSentTo(t, RunConfig{UserAgent: "test", Workers: 1}, "https://example.com/article")
	if _, has := sent["Cookie"]; has {
		t.Error("a run with no cookie configured sent a Cookie header")
	}
}

func TestTheClientKeepsNoCookieJarAndFollowsNoRedirect(t *testing.T) {
	scheduler := newFetchScheduler(RunConfig{Workers: 1})
	if scheduler.client.Jar != nil {
		t.Error("the client has a cookie jar: a Set-Cookie from one answer would be replayed on later requests")
	}
	if scheduler.client.CheckRedirect == nil {
		t.Fatal("the client follows redirects by itself, so a hop would be built where the cookie rule is not asked")
	}
	if err := scheduler.client.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Errorf("CheckRedirect returns %v, want http.ErrUseLastResponse", err)
	}
}
