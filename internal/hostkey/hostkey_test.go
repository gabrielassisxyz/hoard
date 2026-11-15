package hostkey

import (
	"net/url"
	"testing"
)

func keyOf(t *testing.T, address string) string {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse %q: %v", address, err)
	}
	return Of(parsed)
}

func TestOfGivesOneKeyToEverySpellingOfASite(t *testing.T) {
	sameSite := [][]string{
		{"https://example.com/a", "HTTPS://Example.COM:443/b", "https://EXAMPLE.com"},
		{"http://example.com/", "http://example.com:80/x"},
		{"http://[::1]:8080/", "http://[::1]:8080/other"},
	}
	for _, group := range sameSite {
		first := keyOf(t, group[0])
		for _, spelling := range group[1:] {
			if got := keyOf(t, spelling); got != first {
				t.Errorf("Of(%q) = %q, but Of(%q) = %q; they are one site", spelling, got, group[0], first)
			}
		}
	}
}

func TestOfTellsDifferentSitesApart(t *testing.T) {
	differentSites := [][2]string{
		{"https://example.com/", "https://example.com:8443/"},
		{"https://example.com/", "http://example.com:443/"},
		{"https://example.com/", "https://www.example.com/"},
		{"http://127.0.0.1:4000/", "http://127.0.0.1:4001/"},
	}
	for _, pair := range differentSites {
		if a, b := keyOf(t, pair[0]), keyOf(t, pair[1]); a == b {
			t.Errorf("Of(%q) and Of(%q) are both %q; they are different sites", pair[0], pair[1], a)
		}
	}
}

func TestNormalizeMatchesTheKeyOfAnAddressOnThatHost(t *testing.T) {
	cases := []struct{ written, address string }{
		{"Example.COM", "https://example.com/page"},
		{"example.com:8443", "https://example.com:8443/page"},
		{"127.0.0.1:4000", "http://127.0.0.1:4000/"},
		{"[::1]:8080", "http://[::1]:8080/"},
	}
	for _, c := range cases {
		got, err := Normalize(c.written)
		if err != nil {
			t.Errorf("Normalize(%q): %v", c.written, err)
			continue
		}
		if want := keyOf(t, c.address); got != want {
			t.Errorf("Normalize(%q) = %q, but an address on it has the key %q", c.written, got, want)
		}
	}
}

func TestNormalizeRefusesWhatIsNotABareHost(t *testing.T) {
	for _, written := range []string{"", "https://example.com", "example.com/path", "exa mple.com", "user@example.com", "example.com?x=1"} {
		if got, err := Normalize(written); err == nil {
			t.Errorf("Normalize(%q) = %q, want an error", written, got)
		}
	}
}
