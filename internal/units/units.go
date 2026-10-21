// Package units formats sizes, speeds and durations for people.
package units

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Bytes formats n with decimal prefixes, the way drive makers label sizes,
// so a "32 GB" stick shows as 32 GB rather than 29.8 GiB.
func Bytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// Duration formats a remaining time coarsely: "45 s", "3 min", "1 h 5 min".
func Duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()+0.5))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()+0.5))
	}
	h := int(d.Hours())
	return fmt.Sprintf("%d h %d min", h, int(d.Minutes())-60*h)
}

// ParseBytes reads a size such as "8GB", "8G", "512MiB" or "1000000".
// Bare prefixes are decimal, like Bytes; an "iB" suffix makes them binary.
func ParseBytes(s string) (int64, error) {
	t := strings.TrimSpace(s)
	base := int64(1000)
	switch {
	case strings.HasSuffix(t, "iB"):
		base, t = 1024, strings.TrimSuffix(t, "iB")
	case strings.HasSuffix(t, "B"):
		t = strings.TrimSuffix(t, "B")
	}
	mult := int64(1)
	if i := strings.IndexAny(t, "kKMGT"); i >= 0 && i == len(t)-1 {
		for range strings.Index("KMGT", strings.ToUpper(t[i:])) + 1 {
			mult *= base
		}
		t = t[:i]
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	return int64(f * float64(mult)), nil
}
