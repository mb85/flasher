package units

import (
	"testing"
	"time"
)

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1000: "1.0 kB", 32_017_047_552: "32.0 GB", 1_566_638_080: "1.6 GB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{40 * time.Second: "40 s", 150 * time.Second: "3 min", 65 * time.Minute: "1 h 5 min"} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}
