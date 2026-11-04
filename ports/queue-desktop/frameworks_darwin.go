package main

// Wails' macOS code uses UTType from the UniformTypeIdentifiers framework but
// does not link the framework itself; the Wails CLI adds it to CGO_LDFLAGS.
// This links it for a plain go build.

// #cgo LDFLAGS: -framework UniformTypeIdentifiers
import "C"
