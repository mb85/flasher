package ui

import (
	_ "embed"
	"log"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// discFont holds a CD icon drawn in the style of Shirei's Microns icons,
// which have no disc of their own. icons/disc.py builds it.
//
//go:embed icons/disc.ttf
var discFont []byte

var symDisc = IconGlyph{Font: "FlasherIcons", Rune: 0xE000}

func init() {
	if err := UseFontBytes(discFont); err != nil {
		log.Println(err)
	}
}
