// Command desktop is CK Karaoke for Windows, macOS and Linux.
//
// It runs the same server as the phone ports, in-process on a goroutine, bound
// to loopback on a free port. A Wails window shows a startup page from its own
// assets until the server is listening, then navigates to it. From there on the
// window is an ordinary web view onto http://127.0.0.1:<port>/.
//
// Build with the Wails CLI (`wails build`), or with plain Go:
//
//	go build -tags desktop,production -ldflags "-H windowsgui" .
package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend
var frontend embed.FS

func main() {
	assets, err := fs.Sub(frontend, "frontend")
	if err != nil {
		log.Fatal(err)
	}

	app := newApp()
	err = wails.Run(&options.App{
		Title:     "CK Karaoke",
		Width:     480,
		Height:    860,
		MinWidth:  360,
		MinHeight: 480,
		// The startup page's own background, so the window does not flash white
		// before it paints.
		BackgroundColour: &options.RGBA{R: 0x37, G: 0x30, B: 0xA3, A: 0xFF},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []any{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}
