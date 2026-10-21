package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"

	"flasher/internal/drives"
	"flasher/internal/flash"
	"flasher/internal/source"
	"flasher/internal/units"
)

// bigDrive is where a drive stops looking like a USB stick or SD card, so
// the confirmation asks the user to double-check.
const bigDrive = 64e9

func muted() TextStyleFn   { return TextColorVec(CurrentColorScheme.List.Muted) }
func errText() TextStyleFn { return TextColorVec(CurrentColorScheme.List.Error) }

// Root is the frame function.
func (a *App) Root() {
	a.checkFit()
	ModAttrs(UseSurface(SurfaceCanvas))
	Container(Attrs(Viewport, Pad(24), Gap(18)), func() {
		// Key each screen so its widgets never share identity with the
		// previous screen's. Otherwise a press that starts on Cancel and
		// ends after the result screen appears would count as a press of
		// the button now in Cancel's place.
		ContainerWithKey(a.phase, Attrs(Grow(1), Expand), func() {
			switch a.phase {
			case phaseSelect:
				a.selectView()
			case phaseFlashing:
				a.flashingView()
			default:
				a.resultView()
			}
		})
	})
	if a.confirming {
		a.confirmModal()
	}
	if a.browsing {
		a.browserModal()
	}
}

func (a *App) selectView() {
	Container(Attrs(Row, Gap(16), Grow(1), Expand), func() {
		step(1, "Image", a.imageStep)
		step(2, "Drive", a.driveStep)
		step(3, "Flash", a.flashStep)
	})
}

func step(n int, title string, body func()) {
	Container(Attrs(Grow(1), Expand, Extrinsic, Clip, UseSurface(SurfacePanel), Pad(16), Gap(12), Corners(8), BorderWidth(1)), func() {
		// The card's width comes from the window, so hand it down as a
		// max width; otherwise long labels would not wrap.
		if w := GetResolvedWidth(); w > 0 {
			ModAttrs(MaxWidth(w))
		}
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Container(Attrs(FixSize(26, 26), Corners(13), BackgroundVec(CurrentColorScheme.Progress.Fill), Center), func() {
				Label(fmt.Sprint(n), FontWeight(WeightBold), TextColor(0, 0, 100, 1))
			})
			Label(title, FontSize(17), FontWeight(WeightSemibold))
		})
		body()
	})
}

func (a *App) imageStep() {
	switch {
	case a.inspecting:
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			BusyDots()
			Label("Reading image", muted())
		})
		return
	case a.img != nil:
		Label(filepath.Base(a.img.Path), FontWeight(WeightSemibold))
		Label(sentence(a.img.Description()), muted())
		if a.img.Size >= 0 {
			Label(units.Bytes(a.img.Size), muted())
		} else {
			Label(units.Bytes(a.img.FileSize)+" compressed", muted())
		}
		if a.img.Kind == source.KindWindowsISO {
			Label("It will be written as a UEFI-bootable FAT32 installer.", muted())
		}
		if a.img.Kind == source.KindPlainISO {
			Label("This ISO has no partition table, so it will probably not boot from USB.", errText())
		}
	default:
		Label("Choose a disk image or ISO. Compressed images are fine.", muted())
	}
	if a.imgErr != "" {
		Label(sentence(a.imgErr), errText())
	}
	Element(Attrs(Grow(1)))
	label := "Choose image"
	if a.img != nil {
		label = "Change image"
	} else {
		NextButtonType(ButtonPrimary)
	}
	NextAccessName("choose_image")
	NextButtonDisabled(a.picking)
	if Button(SymFile, label) {
		a.pickImage()
	}
}

func (a *App) driveStep() {
	if a.driveErr != "" {
		Label(sentence(a.driveErr), errText())
	} else if len(a.drives) == 0 {
		Label("Plug in a USB drive or SD card.", muted())
	}
	Container(Attrs(Gap(4), Expand), func() {
		for _, d := range a.drives {
			ContainerWithKey(d.Path, Attrs(Pad(8), Corners(6), Gap(2), Expand), func() {
				NextAccessName("drive")
				sel := d.Path == a.selected
				switch {
				case sel:
					ModAttrs(BackgroundVec(CurrentColorScheme.List.Selected.Background), AmendTextStyle(TextColorVec(CurrentColorScheme.List.Selected.Text)))
				case IsHovered():
					ModAttrs(BackgroundVec(CurrentColorScheme.List.Hovered.Background))
				}
				if PressAction() {
					a.selected = d.Path
				}
				Label(d.Name(), FontWeight(WeightSemibold))
				lines := []string{driveDetail(d)}
				if len(d.Labels) > 0 {
					lines = append(lines, strings.Join(d.Labels, ", "))
				}
				for _, l := range lines {
					if sel {
						Label(l)
					} else {
						Label(l, muted())
					}
				}
			})
		}
	})
	Element(Attrs(Grow(1)))
	was := a.showAll
	NextAccessName("show_all")
	CheckBox(&a.showAll, "Show all drives")
	if a.showAll != was {
		go a.refreshDrives()
	}
	if a.showAll {
		Label("Internal drives are listed too. Check twice before flashing.", FontSize(12), errText())
	}
}

// driveWarning cautions about drives that are more likely to hold files
// someone needs than to be a spare stick: internal ones, and USB drives
// larger than sticks usually are.
func driveWarning(d drives.Drive) string {
	switch d.Bus {
	case "usb", "sdio", "sd", "mmc", "loop", "vhd":
	default:
		return fmt.Sprintf("This is an internal %s drive, not a USB stick or memory card. Make sure it is the one you mean.", d.BusName())
	}
	if d.Size > bigDrive {
		return fmt.Sprintf("At %s this is larger than most USB sticks, so it may be an external disk with files on it. Make sure nothing on it is needed.", units.Bytes(d.Size))
	}
	return ""
}

// driveDetail is the line under a drive's name: "32 GB · USB · /dev/sdb".
func driveDetail(d drives.Drive) string {
	parts := []string{units.Bytes(d.Size)}
	if d.Bus != "" {
		parts = append(parts, d.BusName())
	}
	parts = append(parts, d.Ref())
	if d.IsSystem {
		parts = append(parts, "system drive")
	}
	return strings.Join(parts, " · ")
}

func (a *App) flashStep() {
	d, ok := a.selectedDrive()
	switch {
	case a.img == nil && !ok:
		Label("Choose an image and a drive.", muted())
	case a.img == nil:
		Label("Choose an image.", muted())
	case !ok:
		Label("Choose a drive.", muted())
	case a.problem() != "":
		// shown below instead
	case a.fit.pending:
		Label("Checking that the image fits…", muted())
	default:
		Label(fmt.Sprintf("Ready to write %s to %s (%s).", filepath.Base(a.img.Path), d.Name(), d.Ref()), muted())
	}
	if p := a.problem(); p != "" {
		Label(p, errText())
	}
	Element(Attrs(Grow(1)))
	NextAccessName("verify")
	CheckBox(&a.verify, "Verify after writing")
	NextButtonType(ButtonPrimary)
	NextButtonDisabled(!a.canFlash())
	NextAccessName("flash")
	if Button(SymDownload, "Flash") {
		a.confirming = true
	}
}

func (a *App) confirmModal() {
	d, ok := a.selectedDrive()
	if !ok || a.img == nil {
		a.confirming = false
		return
	}
	Modal(440, func() { a.confirming = false }, func() {
		Label("Erase this drive?", FontSize(17), FontWeight(WeightSemibold))
		Container(Attrs(Gap(2)), func() {
			Label(d.Name(), FontWeight(WeightSemibold))
			Label(driveDetail(d), muted())
			if len(d.Labels) > 0 {
				Label("Volumes: "+strings.Join(d.Labels, ", "), muted())
			}
		})
		Label("Everything on it will be replaced by " + filepath.Base(a.img.Path) + ".")
		switch {
		case len(d.Mounts) > 0 && len(d.Labels) > 0:
			Label("Its volumes will be unmounted first.", muted())
		case len(d.Mounts) > 0:
			Label("Its volumes will be unmounted first: "+strings.Join(d.Mounts, ", "), muted())
		}
		if w := driveWarning(d); w != "" {
			Label(w, errText())
		}
		Container(Attrs(Row, Gap(10), MainAlign(AlignEnd), Expand), func() {
			NextAccessName("cancel_flash")
			if Button(NoIcon, "Cancel") {
				a.confirming = false
			}
			NextButtonType(ButtonDestructive)
			NextAccessName("confirm_flash")
			if Button(NoIcon, "Erase and flash") {
				a.confirming = false
				a.startFlash()
			}
		})
	})
}

func (a *App) browserModal() {
	Modal(600, func() { a.browsing = false }, func() {
		Label("Choose an image", FontSize(17), FontWeight(WeightSemibold))
		exts := []string{"img", "iso", "raw", "bin", "xz", "gz", "zst", "bz2", "zip"}
		Container(Attrs(FixHeight(260), Expand, Clip), func() {
			if FileBrowserPanel(&a.browse.cwd, &a.browse.filter, &a.browse.selected, &a.browse.selection, FileBrowserAttrs{Files: true, Exts: exts}) {
				a.browsing = false
				a.inspect(a.browse.selection)
			}
		})
		Container(Attrs(Row, MainAlign(AlignEnd), Expand), func() {
			if Button(NoIcon, "Cancel") {
				a.browsing = false
			}
		})
	})
}

func (a *App) flashingView() {
	d, _ := a.selectedDrive()
	p := a.progress
	Container(Attrs(Grow(1), Expand, Center, Gap(14)), func() {
		title := stageTitle(p.Stage)
		if a.opening {
			title = "Opening the drive"
		}
		Label(title, FontSize(20), FontWeight(WeightSemibold))
		Label(fmt.Sprintf("%s to %s (%s)", filepath.Base(a.img.Path), d.Name(), d.Ref()), muted())
		ProgressBarExt(float32(p.Fraction), ProgressBarAttrs{Width: 420, Height: 10})
		Label(progressLine(p, a.opening), muted())
		NextAccessName("cancel")
		NextButtonDisabled(a.cancel == nil || p.Stage == flash.Syncing)
		if Button(SymCancel, "Cancel") && a.cancel != nil {
			a.cancel()
		}
	})
}

func stageTitle(s flash.Stage) string {
	switch s {
	case flash.Syncing:
		return "Finishing writes"
	case flash.Verifying:
		return "Verifying"
	case flash.Done:
		return "Done"
	}
	return "Writing"
}

func progressLine(p flash.Progress, opening bool) string {
	if opening {
		return "Your system may ask for your password."
	}
	parts := []string{fmt.Sprintf("%.0f%%", p.Fraction*100)}
	if p.BytesPerSec > 0 {
		parts = append(parts, units.Bytes(int64(p.BytesPerSec))+"/s")
	}
	if p.ETA > 0 {
		parts = append(parts, units.Duration(p.ETA)+" left")
	}
	return strings.Join(parts, " · ")
}

func (a *App) resultView() {
	Container(Attrs(Grow(1), Expand, Center, Gap(12)), func() {
		if a.phase == phaseDone {
			Icon(SymPass, FontSize(44), TextColorVec(CurrentColorScheme.Progress.Fill))
			Label("Done", FontSize(20), FontWeight(WeightSemibold))
			Label(fmt.Sprintf("Flashed in %s. You can remove the drive.", units.Duration(a.elapsed.Round(time.Second))), muted())
			NextButtonType(ButtonPrimary)
		} else {
			Icon(SymFail, FontSize(44), errText())
			Label("Flashing did not finish", FontSize(20), FontWeight(WeightSemibold))
			Container(Attrs(MaxWidth(520), Gap(6)), func() {
				Label(a.result, errText())
				if a.resultDetail != "" {
					Label(a.resultDetail, FontSize(12), muted())
				}
			})
		}
		NextAccessName("back")
		if Button(NoIcon, "Flash another") {
			a.phase = phaseSelect
		}
	})
}

// sentence capitalises the first letter of an error message for display.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
