package hostkey

import (
	"net/url"
	"testing"
)

func TestSessionMayGoTo(t *testing.T) {
	cases := []struct {
		name     string
		siteHost string
		address  string
		want     bool
	}{
		{"the site over https", "example.com", "https://example.com/a", true},
		{"the site, another spelling", "example.com", "HTTPS://Example.COM:443/a", true},
		{"the site over plain http", "example.com", "http://example.com/a", false},
		{"a subdomain of the site", "example.com", "https://www.example.com/a", false},
		{"a parent of the site", "www.example.com", "https://example.com/a", false},
		{"another host", "example.com", "https://other.example/a", false},
		{"the site on another port", "example.com", "https://example.com:8443/a", false},
		{"a host that only ends like the site", "example.com", "https://notexample.com/a", false},
		{"a local server over http", "127.0.0.1:4000", "http://127.0.0.1:4000/a", true},
		{"a local server on another port", "127.0.0.1:4000", "http://127.0.0.1:4001/a", false},
		{"localhost over http", "localhost:4000", "http://localhost:4000/a", true},
		{"ipv6 loopback over http", "[::1]:4000", "http://[::1]:4000/a", true},
		{"a private address over http", "192.168.1.10", "http://192.168.1.10/a", false},
		{"no site configured", "", "https://example.com/a", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed, err := url.Parse(c.address)
			if err != nil {
				t.Fatal(err)
			}
			if got := SessionMayGoTo(c.siteHost, parsed); got != c.want {
				t.Errorf("SessionMayGoTo(%q, %q) = %v, want %v", c.siteHost, c.address, got, c.want)
			}
		})
	}
}
