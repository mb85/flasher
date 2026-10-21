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

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{"8GB": 8e9, "8G": 8e9, "512MiB": 512 << 20, "1.5 GB": 1.5e9, "1000000": 1e6, "2TiB": 2 << 40, "4k": 4000} {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Errorf("ParseBytes(%q) = %d, %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "GB", "-1G", "8X"} {
		if _, err := ParseBytes(in); err == nil {
			t.Errorf("ParseBytes(%q) succeeded", in)
		}
	}
}
