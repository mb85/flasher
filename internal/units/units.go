// Package units formats sizes, speeds and durations for people.
package units

import (
	"fmt"
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
