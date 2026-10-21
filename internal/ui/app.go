// Package ui is the Shirei front end: pick an image, pick a drive, flash.
package ui

import (
	"context"
	"errors"
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

	phase    phase
	opening  bool // waiting for the drive to open (and polkit to answer)
	progress flash.Progress
	cancel   context.CancelFunc
	started  time.Time
	elapsed  time.Duration
	result   string
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
		})
		RequestNextFrame()
	}()
}

// pickImage shows the native dialog, falling back to the in-app browser.
func (a *App) pickImage() {
	if a.picking {
		return
	}
	a.picking = true
	start := a.browseStart()
	go func() {
		path, err := filepick.Open("Choose an image to flash")
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
	case a.img.Size > d.Size:
		return "The image is larger than this drive."
	}
	return ""
}

func (a *App) canFlash() bool {
	_, ok := a.selectedDrive()
	return a.img != nil && ok && !a.inspecting && a.problem() == ""
}

// startFlash runs the flash in the background. Called on the UI thread.
func (a *App) startFlash() {
	d, ok := a.selectedDrive()
	if !ok || a.img == nil {
		return
	}
	img, verify := a.img, a.verify
	ctx, cancel := context.WithCancel(context.Background())
	a.phase, a.opening, a.cancel = phaseFlashing, true, cancel
	a.progress = flash.Progress{}
	a.started = time.Now()

	go func() {
		err := a.run(ctx, img, d, verify)
		WithFrameLock(func() {
			a.elapsed = time.Since(a.started)
			a.cancel = nil
			switch {
			case err == nil:
				a.phase = phaseDone
				a.result = ""
			case errors.Is(err, context.Canceled):
				a.phase = phaseFailed
				a.result = "Cancelled. The drive was only partly written, so flash it again or reformat it before using it."
			default:
				a.phase = phaseFailed
				a.result = err.Error()
			}
		})
		RequestNextFrame()
	}()
}

func (a *App) run(ctx context.Context, img *source.Image, d drives.Drive, verify bool) error {
	dev, err := device.Open(d)
	if err != nil {
		return err
	}
	defer dev.Close()
	p, err := plan.For(img, dev.Size())
	if err != nil {
		return err
	}
	WithFrameLock(func() { a.opening = false })
	return flash.Run(ctx, dev, p, flash.Options{
		Verify: verify,
		Progress: func(p flash.Progress) {
			WithFrameLock(func() { a.progress = p })
			RequestNextFrame()
		},
	})
}
