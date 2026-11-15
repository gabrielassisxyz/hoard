package recipe

import (
	"fmt"
	"os"
	"strings"
)

// CookieHeader returns the value of the Cookie request header for this site, or "" when
// the recipe names no cookie file.
//
// The file holds that value on one line; blank lines and lines starting with '#' are
// ignored. No error from here quotes the file's content: an error message ends up in a
// terminal, a log or a pasted report, and the content is a session.
func (r Recipe) CookieHeader() (string, error) {
	path := r.Fetch.CookieFile
	if path == "" {
		return "", nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("cookie file: %w", err)
	}
	// The same bargain ssh makes with a private key: a session other accounts can read is
	// refused rather than used.
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return "", fmt.Errorf("cookie file %s is readable by others (mode %04o): chmod 600 it", path, mode)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cookie file: %w", err)
	}
	var values []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		values = append(values, line)
	}
	switch len(values) {
	case 0:
		return "", fmt.Errorf("cookie file %s holds no cookie", path)
	case 1:
		return values[0], nil
	}
	return "", fmt.Errorf("cookie file %s holds %d lines of value, and the header is one line", path, len(values))
}
