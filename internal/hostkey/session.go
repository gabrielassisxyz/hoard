package hostkey

import (
	"net"
	"net/url"
	"strings"
)

// SessionMayGoTo says whether a request to u may carry the session that belongs to
// siteHost. It may when u is on that site and the session cannot be read on the way:
// over https, or to this machine itself.
//
// Every request is asked, not every address: a redirect is a request of its own, and a
// session that follows one to another host, or drops to plain http on the first hop of an
// http-to-https redirect, has leaked without anything failing.
func SessionMayGoTo(siteHost string, u *url.URL) bool {
	if siteHost == "" || Of(u) != siteHost {
		return false
	}
	return strings.EqualFold(u.Scheme, "https") || isLoopback(u.Hostname())
}

// The web platform's own exception: an origin on this machine is as trustworthy as an
// encrypted one, since nothing travels a network. It is also what lets a test stand in for
// a site with a plain local server.
func isLoopback(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}
