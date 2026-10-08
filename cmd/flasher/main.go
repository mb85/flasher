// Command flasher is the desktop app for writing images to USB drives.
package main

import (
	"flag"
	"fmt"
	"os"

	"go.hasen.dev/shirei/app"
	"go.hasen.dev/shirei/ext/darkmode"
	"go.hasen.dev/shirei/ext/window"

	. "go.hasen.dev/shirei/widgets"

	"flasher/internal/ui"
)

func main() {
	loop := flag.Bool("include-loop", false, "list loop devices as targets (for testing)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: flasher [-include-loop] [IMAGE]")
		flag.PrintDefaults()
	}
	flag.Parse()

	a := ui.New(ui.Options{IncludeLoop: *loop, Image: flag.Arg(0)})
	app.SetupWindow("Flasher", 840, 460)
	window.SetMinSize(720, 420)
	app.SetupDrive()
	a.Start()
	app.Run(func() {
		SetDarkMode(darkmode.OSDarkMode())
		a.Root()
	})
}
