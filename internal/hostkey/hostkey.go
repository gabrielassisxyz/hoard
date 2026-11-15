// Package hostkey decides what counts as one site. The fetcher paces requests per site and
// a recipe names the one site it describes, so both must agree on when two addresses are
// the same site and when they are not.
package hostkey

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Of names the site a request is sent to: the host and port, in lower case, with the port
// left out when it is the scheme's own. Two spellings of one site must share a key, or a
// stated pace would quietly be doubled.
func Of(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" || port == defaultPortOf(u.Scheme) {
		return host
	}
	return net.JoinHostPort(host, port)
}

func defaultPortOf(scheme string) string {
	switch strings.ToLower(scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// Normalize turns a host as a person writes it, "Example.com" or "example.com:8443", into
// the key Of gives an address on that host. Only the case changes: a port is kept as
// written, so it belongs in the text only when it is not the scheme's default.
func Normalize(host string) (string, error) {
	if host == "" {
		return "", errors.New("is empty")
	}
	if strings.ContainsAny(host, "/ \t\r\n?#@") {
		return "", fmt.Errorf("%q is not a bare host: write it without a scheme, a path or spaces", host)
	}
	parsed, err := url.Parse("//" + host)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("%q is not a host", host)
	}
	name := strings.ToLower(parsed.Hostname())
	if parsed.Port() == "" {
		return name, nil
	}
	return net.JoinHostPort(name, parsed.Port()), nil
}
