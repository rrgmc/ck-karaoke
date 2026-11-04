// Command winres writes the Windows resources of the tray app: its icon, as
// rsrc_windows_amd64.syso, which go build links into the Windows executable.
//
//	go generate .   (from ports/queue-desktop)
//
// Explorer, the taskbar and the configuration window show this icon. Wails
// loads the window's icon from the icon group with id 3, so the group gets
// that id. Run it again after go run ./internal/cmd/genicon changes
// appicon.ico.
package main

import (
	"fmt"
	"os"

	"github.com/tc-hib/winres"
)

func main() {
	if err := run("appicon.ico", "rsrc_windows_amd64.syso"); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(icon, out string) error {
	in, err := os.Open(icon)
	if err != nil {
		return err
	}
	defer in.Close()
	ico, err := winres.LoadICO(in)
	if err != nil {
		return fmt.Errorf("reading %s: %w", icon, err)
	}
	var rs winres.ResourceSet
	// Wails' winc.AppIconID.
	if err := rs.SetIcon(winres.ID(3), ico); err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	if err := rs.WriteObject(f, winres.ArchAMD64); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
