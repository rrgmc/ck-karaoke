//go:build queue

package main

import (
	"github.com/rrgmc/ck-karaoke/i18n"
	"github.com/rrgmc/ck-karaoke/server"
	"github.com/wailsapp/wails/v2/pkg/options"
)

// CK Queue Remote, the guest app for a CK Queue Manager. Its backend is fixed
// here, at build time, so it has no way to reach the unit around the queue
// manager. It keeps its own data directory, so its favorites stay apart from
// CK Karaoke's.
var variant = appVariant{
	name:         "CK Queue Remote",
	backend:      server.BackendQueue,
	dataDir:      "CK Queue Remote",
	lastAddrFile: "queue-manager.addr",
	background:   options.RGBA{R: 0xB4, G: 0x53, B: 0x09, A: 0xFF},
	gradient:     "linear-gradient(135deg, #B45309, #BE123C)",
	text: func(lang i18n.Lang) startupText {
		return startupText{
			looking:     lang.T("Looking for the queue manager on this network…"),
			hint:        lang.T("No queue manager has been found yet."),
			downloading: lang.T("Downloading the song list from the queue manager…"),
			dialogTitle: lang.T("CK Queue Remote stopped"),
		}
	},
}
