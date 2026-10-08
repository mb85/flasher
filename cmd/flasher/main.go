package main

import (
	"flag"
	"fmt"

	app "go.hasen.dev/shirei/app"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

var progress float32

func main() {
	png := flag.String("png", "", "render the window to a PNG and exit")
	flag.Parse()
	if *png != "" {
		// Headless renders must wait for the font scan or no text is drawn.
		InitFontSubsystem()
		WaitForSystemFontScan()
		if err := RenderToPNG(*png, 480, 160, root); err != nil {
			panic(err)
		}
		return
	}
	app.SetupWindow("Flasher", 480, 160)
	app.Run(root)
}

func root() {
	Container(Attrs(Viewport, Pad(20), Gap(12)), func() {
		Label("Flasher spike", FontSize(20), FontWeight(WeightBold))
		ProgressBar(progress)
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			if Button(SymIPlus, "Advance") {
				progress = min(progress+0.1, 1)
			}
			Label(fmt.Sprintf("%.0f%%", progress*100))
		})
	})
}
