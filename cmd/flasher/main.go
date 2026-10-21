// Command flasher is the desktop app for writing images to USB drives.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"

	"go.hasen.dev/shirei/app"
	"go.hasen.dev/shirei/ext/darkmode"
	"go.hasen.dev/shirei/ext/window"

	. "go.hasen.dev/shirei/widgets"

	"flasher/internal/ui"
)

// icon is shown in the title bar and taskbar; the .exe's own icon comes from
// the go-winres resources built from the same image.
//
//go:embed winres/icon.png
var icon []byte

func main() {
	var virtual bool
	flag.BoolVar(&virtual, "include-virtual", false, "list loop devices (Linux) and attached VHDs (Windows) as targets, for testing")
	flag.BoolVar(&virtual, "include-loop", false, "same as -include-virtual")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: flasher [-include-virtual] [IMAGE]")
		flag.PrintDefaults()
	}
	flag.Parse()

	a := ui.New(ui.Options{IncludeVirtual: virtual, Image: flag.Arg(0)})
	app.SetupWindow("Flasher", 840, 460)
	app.SetupIconBytes(icon)
	window.SetMinSize(720, 420)
	app.SetupDrive()
	a.Start()
	app.Run(func() {
		SetDarkMode(darkmode.OSDarkMode())
		a.Root()
	})
}
