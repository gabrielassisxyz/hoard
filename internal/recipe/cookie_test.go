package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Distinctive enough that finding it in an error message cannot be a coincidence.
const cookieValueUnderTest = "session=s3cr3t-value-7f3a; theme=dark"

func recipeWithCookieFile(t *testing.T, content string, mode os.FileMode) Recipe {
	t.Helper()
	path := filepath.Join(t.TempDir(), "site.cookie")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode passes through the umask; the test needs the exact bits.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return Recipe{Fetch: Fetch{CookieFile: path}}
}

func TestCookieHeaderIsEmptyWithoutACookieFile(t *testing.T) {
	value, err := Recipe{}.CookieHeader()
	if value != "" || err != nil {
		t.Errorf("CookieHeader() = %q, %v; want no cookie and no error", value, err)
	}
}

func TestCookieHeaderReturnsTheOneValueLine(t *testing.T) {
	content := "# exported 2026-01-01\n\n  " + cookieValueUnderTest + "  \n\n"
	value, err := recipeWithCookieFile(t, content, 0o600).CookieHeader()
	if err != nil {
		t.Fatalf("CookieHeader: %v", err)
	}
	if value != cookieValueUnderTest {
		t.Errorf("CookieHeader() = %q, want the value line trimmed", value)
	}
}

func TestCookieHeaderRefusesAFileItCannotUse(t *testing.T) {
	cases := []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{"no value line", "# only a comment\n\n", 0o600},
		{"two value lines", cookieValueUnderTest + "\nother=1\n", 0o600},
		{"readable by the group", cookieValueUnderTest + "\n", 0o640},
		{"readable by everyone", cookieValueUnderTest + "\n", 0o604},
		{"writable by the group", cookieValueUnderTest + "\n", 0o620},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			value, err := recipeWithCookieFile(t, c.content, c.mode).CookieHeader()
			if err == nil {
				t.Fatalf("CookieHeader() accepted the file and returned %d bytes", len(value))
			}
			if value != "" {
				t.Errorf("CookieHeader() returned a value alongside its error")
			}
			if strings.Contains(err.Error(), "s3cr3t") {
				t.Errorf("the error quotes the cookie: %v", err)
			}
		})
	}
}

func TestCookieHeaderSaysWhenTheFileIsMissing(t *testing.T) {
	missing := Recipe{Fetch: Fetch{CookieFile: filepath.Join(t.TempDir(), "absent.cookie")}}
	if _, err := missing.CookieHeader(); err == nil {
		t.Error("CookieHeader() of a missing file returned no error")
	}
}
