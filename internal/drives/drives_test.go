package drives

import "testing"

func TestEligible(t *testing.T) {
	cases := []struct {
		name string
		d    Drive
		def  bool // in the default list
		all  bool // with All set
	}{
		{"usb stick", Drive{Bus: "usb", Size: 32e9}, true, true},
		{"sd card", Drive{Bus: "sdio", Size: 32e9}, true, true},
		{"removable ata", Drive{Bus: "ata", Removable: true, Size: 1e9}, true, true},
		{"internal ssd", Drive{Bus: "nvme", Size: 1e12}, false, true},
		{"usb system disk", Drive{Bus: "usb", Size: 1e12, IsSystem: true}, false, true},
		{"empty card reader", Drive{Bus: "usb"}, false, true},
		{"loop", Drive{Bus: "loop", Size: 1e9}, false, false},
	}
	for _, c := range cases {
		if got := c.d.eligible(Options{}); got != c.def {
			t.Errorf("%s: default list %v, want %v", c.name, got, c.def)
		}
		if got := c.d.eligible(Options{All: true}); got != c.all {
			t.Errorf("%s: with All %v, want %v", c.name, got, c.all)
		}
	}
	if !(Drive{Bus: "loop", Size: 1}).eligible(Options{IncludeVirtual: true}) {
		t.Error("loop devices should appear with IncludeVirtual")
	}
}

func TestNormalisePath(t *testing.T) {
	for in, want := range map[string]string{
		"/dev/sdb":           "/dev/sdb",
		"sdb":                "/dev/sdb",
		"PhysicalDrive2":     `\\.\PhysicalDrive2`,
		"//./PhysicalDrive2": `\\.\PhysicalDrive2`,
		`\\.\PhysicalDrive2`: `\\.\PhysicalDrive2`,
	} {
		if got := normalisePath(in); got != want {
			t.Errorf("normalisePath(%q) = %q, want %q", in, got, want)
		}
	}
}
