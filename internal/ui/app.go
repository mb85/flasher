// Package ui is the Shirei front end: pick an image, pick a drive, flash.
package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "go.hasen.dev/shirei"

	"flasher/internal/device"
	"flasher/internal/drives"
	"flasher/internal/filepick"
	"flasher/internal/flash"
	"flasher/internal/plan"
	"flasher/internal/source"
	"flasher/internal/units"
)

// Options configures the app at start-up.
type Options struct {
	IncludeVirtual bool   // list loop devices and VHDs, for testing
	Image          string // image to preselect, from the command line
}

type phase int

const (
	phaseSelect phase = iota
	phaseFlashing
	phaseDone
	phaseFailed
)

// App holds all UI state. Background goroutines change it only under
// WithFrameLock and then call RequestNextFrame.
type App struct {
	opts Options

	img        *source.Image
	imgErr     string
	inspecting bool
	picking    bool

	browsing bool // the in-app file browser, used when no native dialog works
	browse   struct {
		cwd, filter, selection string
		selected               int
	}

	drives   []drives.Drive
	driveErr string
	selected string // path of the chosen drive
	showAll  bool

	verify     bool
	confirming bool
	fit        fitCheck

	// Choices for Linux ISOs that can be copied file by file.
	copyFiles bool
	persist   bool
	persistGB float32

	phase        phase
	opening      bool // waiting for the drive to open (and polkit to answer)
	progress     flash.Progress
	cancel       context.CancelFunc
	started      time.Time
	elapsed      time.Duration
	result       string // what went wrong, in plain words
	resultDetail string // the technical error behind it
}

// fitCheck is the outcome of planning the image onto the chosen drive.
// Windows media and copied Linux ISOs are rebuilt, so planning them is the
// only exact way to know they fit; the plan is made in the background
// whenever the image, drive or options change.
type fitCheck struct {
	key     string // image path, drive path and size the result is for
	pending bool
	err     string
}

func fitKey(img *source.Image, d drives.Drive, opts plan.Options) string {
	return fmt.Sprintf("%s|%s|%d|%d|%v", img.Path, d.Path, d.Size, d.SectorSize, opts)
}

// rebuilt reports whether the image is laid out file by file rather than
// written as it is, so only a plan can tell whether it fits.
func (a *App) rebuilt() bool {
	return a.img != nil && (a.img.Kind == source.KindWindowsISO || a.copyMode())
}

func (a *App) copyMode() bool { return a.copyFiles && a.img != nil && a.img.Linux != nil }

// persistence reports whether the image can keep a live system's changes
// when copied.
func (a *App) persistence() bool {
	return a.copyMode() && a.img.Linux.Persistence != source.NoPersistence
}

// maxPersistGB is the most persistence, in whole GB, that leaves room for
// the ISO's files on d, or 0 when there is not room for the minimum.
func (a *App) maxPersistGB(d drives.Drive) float32 {
	files := a.img.Linux.Bytes + a.img.Linux.Bytes/32 + 256e6 // cluster slack and FAT32 overhead
	n := (d.Size - files - 2<<20) / 1e9
	if n*1e9 < plan.MinPersistence {
		return 0
	}
	return float32(n)
}

// planOptions turns the choices made for the image into plan options.
func (a *App) planOptions() plan.Options {
	opts := plan.Options{CopyFiles: a.copyMode()}
	if d, ok := a.selectedDrive(); ok && a.persist && a.persistence() {
		if gb := min(a.persistGB, a.maxPersistGB(d)); gb >= 1 {
			opts.Persistence = int64(gb) * 1e9
		}
	}
	return opts
}

// checkFit starts planning the image onto the selected drive when that pair
// or the options have changed. Called on the UI thread.
func (a *App) checkFit() {
	d, ok := a.selectedDrive()
	if !ok || !a.rebuilt() {
		a.fit = fitCheck{}
		return
	}
	opts := a.planOptions()
	key := fitKey(a.img, d, opts)
	if a.fit.key == key {
		return
	}
	a.fit = fitCheck{key: key, pending: true}
	img := a.img
	go func() {
		msg := ""
		p, err := plan.For(img, d.Size, d.SectorSize, opts)
		if err != nil {
			msg = sentence(err.Error()) + "."
		} else {
			p.Close()
		}
		WithFrameLock(func() {
			if a.fit.key == key {
				a.fit.pending, a.fit.err = false, msg
			}
		})
		RequestNextFrame()
	}()
}

// New creates the app. Call Start once the window is set up.
func New(opts Options) *App {
	return &App{opts: opts, verify: true}
}

// Start begins polling for drives and inspects any image given on the
// command line.
func (a *App) Start() {
	go a.pollDrives()
	if a.opts.Image != "" {
		WithFrameLock(func() { a.inspect(a.opts.Image) })
		RequestNextFrame()
	}
}

func (a *App) pollDrives() {
	for {
		a.refreshDrives()
		time.Sleep(2 * time.Second)
	}
}

func (a *App) refreshDrives() {
	var showAll bool
	WithFrameLock(func() { showAll = a.showAll })
	ds, err := drives.List(drives.Options{All: showAll, IncludeVirtual: a.opts.IncludeVirtual})
	WithFrameLock(func() {
		a.drives = ds
		a.driveErr = ""
		if err != nil {
			a.driveErr = err.Error()
		}
	})
	RequestNextFrame()
}

// inspect reads an image's header in the background. Call it on the UI
// thread or under the frame lock.
func (a *App) inspect(path string) {
	a.inspecting, a.imgErr = true, ""
	go func() {
		img, err := source.Inspect(path)
		WithFrameLock(func() {
			a.inspecting = false
			if err != nil {
				a.imgErr = err.Error()
				return
			}
			a.img = img
			a.copyFiles, a.persist, a.persistGB = false, false, 4
		})
		RequestNextFrame()
	}()
}

// takesDrops reports whether a file dropped on the window would become the
// image: only while choosing, with no dialog open or image being read.
func (a *App) takesDrops() bool {
	return a.phase == phaseSelect && !a.confirming && !a.browsing && !a.picking && !a.inspecting
}

// pickImage shows the native dialog, falling back to the in-app browser.
func (a *App) pickImage() {
	if a.picking {
		return
	}
	a.picking = true
	start := a.browseStart()
	parent := dialogParent()
	go func() {
		path, err := filepick.Open("Choose an image to flash", parent)
		WithFrameLock(func() {
			a.picking = false
			if errors.Is(err, filepick.ErrUnavailable) {
				a.browsing = true
				a.browse.cwd, a.browse.filter, a.browse.selected = start, "", -1
			}
			if err == nil {
				a.inspect(path)
			}
		})
		RequestNextFrame()
	}()
}

func (a *App) browseStart() string {
	if a.img != nil {
		return filepath.Dir(a.img.Path)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/"
}

func (a *App) selectedDrive() (drives.Drive, bool) {
	for _, d := range a.drives {
		if d.Path == a.selected {
			return d, true
		}
	}
	return drives.Drive{}, false
}

// problem explains why the current choice cannot be flashed, if it can't.
func (a *App) problem() string {
	d, ok := a.selectedDrive()
	if a.img == nil || !ok {
		return ""
	}
	switch {
	case d.IsSystem:
		return "This drive holds the running system."
	case a.rebuilt():
		return a.fit.err
	case a.img.Size > d.Size:
		return fmt.Sprintf("The image is %s and this drive holds %s.", units.Bytes(a.img.Size), units.Bytes(d.Size))
	}
	return ""
}

func (a *App) canFlash() bool {
	_, ok := a.selectedDrive()
	return a.img != nil && ok && !a.inspecting && !a.fit.pending && a.problem() == ""
}

// startFlash runs the flash in the background. Called on the UI thread.
func (a *App) startFlash() {
	d, ok := a.selectedDrive()
	if !ok || a.img == nil {
		return
	}
	img, verify, opts := a.img, a.verify, a.planOptions()
	ctx, cancel := context.WithCancel(context.Background())
	a.phase, a.opening, a.cancel = phaseFlashing, true, cancel
	a.progress = flash.Progress{}
	a.started = time.Now()

	go func() {
		err := a.run(ctx, img, d, opts, verify)
		WithFrameLock(func() {
			a.elapsed = time.Since(a.started)
			a.cancel = nil
			if err == nil {
				a.phase, a.result, a.resultDetail = phaseDone, "", ""
			} else {
				a.phase = phaseFailed
				a.result, a.resultDetail = flash.Explain(err)
			}
		})
		RequestNextFrame()
	}()
}

func (a *App) run(ctx context.Context, img *source.Image, d drives.Drive, opts plan.Options, verify bool) error {
	// Plan before opening: opening a drive on Windows already clears its
	// partition table, and a plan can still fail (an image too large).
	p, err := plan.For(img, d.Size, d.SectorSize, opts)
	if err != nil {
		return err
	}
	defer p.Close()
	dev, err := device.Open(d)
	if err != nil {
		return err
	}
	defer dev.Close()
	WithFrameLock(func() { a.opening = false })
	return flash.Run(ctx, dev, p, flash.Options{
		Verify: verify,
		Progress: func(p flash.Progress) {
			WithFrameLock(func() { a.progress = p })
			RequestNextFrame()
		},
	})
}
