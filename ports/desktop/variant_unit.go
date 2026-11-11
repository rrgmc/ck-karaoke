//go:build !queue

package main

import (
	"github.com/rrgmc/ck-karaoke/i18n"
	"github.com/rrgmc/ck-karaoke/server"
	"github.com/wailsapp/wails/v2/pkg/options"
)

// CK Karaoke, which drives the karaoke unit. Build with -tags queue for
// CK Queue Remote; variant_queue.go has the other side of each value.
var variant = appVariant{
	name:         "CK Karaoke",
	backend:      server.BackendUnit,
	dataDir:      "CK Karaoke",
	lastAddrFile: "device.addr",
	background:   options.RGBA{R: 0x37, G: 0x30, B: 0xA3, A: 0xFF},
	gradient:     "linear-gradient(135deg, #3730A3, #0D9488)",
	text: func(lang i18n.Lang) startupText {
		return startupText{
			looking:     lang.T("Looking for your karaoke device…"),
			hint:        lang.T("No device has answered yet. Check that it is switched on and on the same network as this computer. On a Mac, also check that CK Karaoke is allowed in System Settings, Privacy & Security, Local Network."),
			downloading: lang.T("Downloading the song list…"),
			dialogTitle: lang.T("CK Karaoke stopped"),
		}
	},
}
