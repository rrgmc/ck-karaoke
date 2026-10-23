package server

import (
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/rrgmc/ck-karaoke/client"
)

// fallbackAddr is the address to fall back to when discovery finds nothing, or
// "" if there is nothing sensible to fall back to.
//
// Three conditions, all necessary:
//
//   - The failure has to be a silent network rather than something else. A
//     malformed address or a broken socket is a real error and should surface.
//   - There has to be a remembered address, which there is only after a run that
//     did reach the device.
//   - The catalogue has to already exist. Without it the device is genuinely
//     required — it is the only place the song list comes from — so pretending we
//     can carry on would just move the failure somewhere less clear.
func fallbackAddr(err error, lastAddrFile, songDB string) string {
	if !errors.Is(err, client.ErrNoDevice) || lastAddrFile == "" || songDB == "" {
		return ""
	}
	if _, statErr := os.Stat(songDB); statErr != nil {
		return ""
	}
	return readAddr(lastAddrFile)
}

// readAddr returns the remembered address, or "" if there is none or it is not
// one. Validated on the way out rather than trusted: this file is on disk between
// runs and a half-written or hand-edited one must not become a dial target.
func readAddr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	addr := strings.TrimSpace(string(b))
	if addr == "" {
		return ""
	}
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil || net.ParseIP(host) == nil {
		return ""
	}
	return addr
}

// rememberAddr records where the device was found, for the next start.
//
// Best effort: failing to write it costs the next start its offline fallback,
// which is not worth refusing to serve over. Written through a temporary file so
// a start interrupted mid-write cannot leave a truncated address behind.
func rememberAddr(path, addr string, logger *slog.Logger) {
	if readAddr(path) == addr {
		return // unchanged; leave the file and its mtime alone
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".device.addr-*")
	if err != nil {
		logger.Debug("cannot remember the device address", "file", path, "error", err)
		return
	}
	name := tmp.Name()
	_, writeErr := tmp.WriteString(addr + "\n")
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(name)
		logger.Debug("cannot remember the device address", "file", path,
			"error", errors.Join(writeErr, closeErr))
		return
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		logger.Debug("cannot remember the device address", "file", path, "error", err)
	}
}
