package wa

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Archive retention bounds. A TTL is mandatory: there is no "keep forever",
// because the archive holds other people's messages.
const (
	DefaultTTL = 14 * 24 * time.Hour
	MinTTL     = time.Hour
	MaxTTL     = 90 * 24 * time.Hour
)

// ParseDuration accepts Go durations plus a day suffix ("14d", "36h",
// "1d12h" is not supported — keep it to one unit when using days).
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q: want e.g. 14d, 36h, 90m", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid duration %q: want e.g. 14d, 36h, 90m", s)
	}
	return d, nil
}

// ParseTTL parses s and enforces [MinTTL, MaxTTL].
func ParseTTL(s string) (time.Duration, error) {
	d, err := ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < MinTTL || d > MaxTTL {
		return 0, fmt.Errorf("ttl %s out of range: must be between %s and %s", s, FormatDuration(MinTTL), FormatDuration(MaxTTL))
	}
	return d, nil
}

// FormatDuration renders whole days as "Nd", whole hours as "Nh" and
// anything else in Go form.
func FormatDuration(d time.Duration) string {
	day := 24 * time.Hour
	switch {
	case d > 0 && d%day == 0:
		return fmt.Sprintf("%dd", d/day)
	case d > 0 && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	default:
		return d.String()
	}
}
