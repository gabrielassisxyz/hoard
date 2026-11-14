package fetch

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// What a host is given to cool down when its refusal names no time of its own.
	defaultRefusalPause = 30 * time.Second
	// The longest a host is ever paused for one refusal, however many came in a row.
	maxRefusalPause = 10 * time.Minute
	// How many refusals in a row end the run's dealings with a host. With the default pause
	// that is seven and a half minutes of waiting before the host is left for a later run.
	maxRefusalsInARow = 5
)

// ParseRetryAfter reads a Retry-After header, which is either a whole number of seconds
// or an HTTP date. A date already past reads as no wait at all. The second result is false
// when the header is absent or is neither form, so the caller can fall back to its own
// pause instead of treating garbage as "retry now".
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if isAllDigits(value) {
		seconds, err := strconv.ParseUint(value, 10, 63)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return 0, false
		}
		// A number too large for a Duration is capped well above any pause that will be
		// applied, instead of overflowing into a negative one.
		if err != nil || seconds > math.MaxInt64/uint64(time.Second) {
			return time.Duration(math.MaxInt64), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	if wait := when.Sub(now); wait > 0 {
		return wait, true
	}
	return 0, true
}

// refusalPause is how long a host rests after its streak-th refusal in a row: the base
// pause, doubled for each refusal after the first, never more than maxRefusalPause.
func refusalPause(base time.Duration, streak int) time.Duration {
	pause := base
	for i := 1; i < streak && pause < maxRefusalPause; i++ {
		pause *= 2
	}
	return min(pause, maxRefusalPause)
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}
