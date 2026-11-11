package main

import (
	"github.com/rrgmc/ck-karaoke/i18n"
	"github.com/wailsapp/wails/v2/pkg/options"
)

// appVariant is what differs between the two apps this module builds:
// CK Karaoke by default, CK Queue Remote with -tags queue.
type appVariant struct {
	name    string // the window title and the startup page's heading
	backend string // server.Config.Backend
	// dataDir is the directory under the user's configuration directory that
	// holds the databases.
	dataDir      string
	lastAddrFile string
	// background is the window colour before the startup page paints, the
	// first colour of gradient, the startup page's background.
	background options.RGBA
	gradient   string
	text       func(i18n.Lang) startupText
}

// startupText is the startup page's text and the native error dialog's title.
type startupText struct {
	looking, hint, downloading, dialogTitle string
}
