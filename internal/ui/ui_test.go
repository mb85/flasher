package ui

import (
	"testing"
	"time"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"

	"flasher/internal/drives"
	"flasher/internal/flash"
	"flasher/internal/source"
)

var (
	stick  = drives.Drive{Path: "/dev/sdb", Vendor: "SanDisk", Model: "Ultra", Size: 32e9, Bus: "usb", Removable: true}
	card   = drives.Drive{Path: "/dev/mmcblk0", Vendor: "", Model: "SD card", Size: 64e9, Bus: "sdio", Mounts: []string{"/run/media/user/bootfs"}, Labels: []string{"bootfs", "rootfs"}}
	winUSB = drives.Drive{Path: `\\.\PhysicalDrive2`, ID: "2", Vendor: "SanDisk", Model: "SanDisk 3.2 Gen 1", Size: 250e9, Bus: "usb", Mounts: []string{`E:\`}, Labels: []string{"SSD (E:)"}}
	ssd    = drives.Drive{Path: "/dev/nvme1n1", Vendor: "Samsung", Model: "990 PRO", Size: 2e12, Bus: "nvme"}
	pi     = &source.Image{Path: "/images/raspios-lite.img.xz", FileSize: 480e6, Compression: source.Xz, Kind: source.KindRaw, Size: -1}
	win    = &source.Image{Path: "/images/Win11_25H2_English_x64.iso", FileSize: 8_486_862_848, Kind: source.KindWindowsISO, Size: 8_486_862_848, Label: "CCCOMA_X64FRE_EN-GB_DV9"}
	big    = &source.Image{Path: "/images/huge.img", FileSize: 40e9, Kind: source.KindRaw, Size: 40e9}
)

func TestSnapshots(t *testing.T) {
	cases := []struct {
		name string
		dark bool
		app  func() *App
	}{
		{"empty", false, func() *App { return &App{verify: true} }},
		{"ready", false, func() *App {
			return &App{verify: true, img: pi, drives: []drives.Drive{stick, card}, selected: stick.Path}
		}},
		{"ready_dark", true, func() *App {
			return &App{verify: true, img: pi, drives: []drives.Drive{stick, card}, selected: stick.Path}
		}},
		{"windows_iso", false, func() *App {
			return &App{verify: true, img: win, drives: []drives.Drive{stick}, selected: stick.Path, fit: fitCheck{key: fitKey(win, stick)}}
		}},
		{"windows_too_small", false, func() *App {
			small := drives.Drive{Path: "/dev/sdc", Vendor: "Kingston", Model: "DataTraveler", Size: 8e9, Bus: "usb", Removable: true}
			return &App{verify: true, img: win, drives: []drives.Drive{small}, selected: small.Path,
				fit: fitCheck{key: fitKey(win, small), err: "The drive is too small for this Windows image: its files need 8.5 GB and the drive holds 8.0 GB."}}
		}},
		{"too_large", false, func() *App {
			return &App{verify: true, img: big, drives: []drives.Drive{stick}, selected: stick.Path}
		}},
		{"image_error", false, func() *App {
			return &App{verify: true, imgErr: "zip archive holds more than one file (a.img and b.img)", drives: []drives.Drive{stick}}
		}},
		{"show_all", false, func() *App {
			return &App{verify: true, img: pi, drives: []drives.Drive{stick, ssd}, selected: ssd.Path, showAll: true}
		}},
		{"confirm", false, func() *App {
			return &App{verify: true, img: pi, drives: []drives.Drive{card}, selected: card.Path, confirming: true}
		}},
		{"windows_drive", false, func() *App {
			return &App{verify: true, img: pi, drives: []drives.Drive{winUSB, card}, selected: winUSB.Path}
		}},
		{"confirm_windows", false, func() *App {
			return &App{verify: true, img: pi, drives: []drives.Drive{winUSB}, selected: winUSB.Path, confirming: true}
		}},
		{"confirm_internal", false, func() *App {
			return &App{verify: true, img: pi, drives: []drives.Drive{ssd}, selected: ssd.Path, showAll: true, confirming: true}
		}},
		{"opening", false, func() *App {
			return &App{img: pi, drives: []drives.Drive{stick}, selected: stick.Path, phase: phaseFlashing, opening: true, cancel: func() {}}
		}},
		{"writing", false, func() *App {
			return &App{img: pi, drives: []drives.Drive{stick}, selected: stick.Path, phase: phaseFlashing, cancel: func() {},
				progress: flash.Progress{Stage: flash.Writing, Bytes: 1.1e9, Fraction: 0.42, BytesPerSec: 35.2e6, ETA: 3 * time.Minute}}
		}},
		{"verifying", false, func() *App {
			return &App{img: pi, drives: []drives.Drive{stick}, selected: stick.Path, phase: phaseFlashing, cancel: func() {},
				progress: flash.Progress{Stage: flash.Verifying, Bytes: 2e9, Fraction: 0.8, BytesPerSec: 90e6, ETA: 10 * time.Second}}
		}},
		{"done", false, func() *App {
			return &App{img: pi, drives: []drives.Drive{stick}, selected: stick.Path, phase: phaseDone, elapsed: 245 * time.Second}
		}},
		{"failed", false, func() *App {
			a := &App{img: pi, drives: []drives.Drive{stick}, selected: stick.Path, phase: phaseFailed}
			a.result, a.resultDetail = flash.Explain(&flash.VerifyError{Segment: "image", Offset: 1 << 30})
			return a
		}},
		{"browser", false, func() *App {
			a := &App{verify: true, browsing: true, drives: []drives.Drive{stick}}
			a.browse.cwd, a.browse.selected = "testdata/browse/images", -1
			return a
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := c.app()
			r := Snapshot(t.Name(), c.name, 840, 460, func() {
				SetDarkMode(c.dark)
				a.Root()
			})
			if r.Err != nil {
				t.Fatal(r.Err)
			}
			if r.Status == SnapMismatch {
				t.Fatalf("snapshot differs: %s", r.Actual)
			}
		})
	}
	SetDarkMode(false)
}

func TestProblem(t *testing.T) {
	a := &App{img: big, drives: []drives.Drive{stick}, selected: stick.Path}
	if a.problem() == "" || a.canFlash() {
		t.Error("a 40 GB image must not be flashable to a 32 GB stick")
	}
	sys := stick
	sys.IsSystem = true
	a = &App{img: pi, drives: []drives.Drive{sys}, selected: sys.Path}
	if a.canFlash() {
		t.Error("the system drive must never be flashable")
	}
	a = &App{img: pi, drives: []drives.Drive{stick}, selected: "/dev/sdz"}
	if a.canFlash() {
		t.Error("a drive that has disappeared must not be flashable")
	}
}
