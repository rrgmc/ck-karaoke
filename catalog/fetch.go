package catalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rrgmc/ck-karaoke/client"
)

// Downloader is the part of *client.Client that Fetch needs.
type Downloader interface {
	DownloadDatabase(ctx context.Context, sink func(chunk []byte) error, opts client.DownloadOptions) (int, error)
}

// Progress reports transfer progress. total is 0 until the first chunk reveals
// the database's declared size.
type Progress func(have, total int)

// Fetch downloads the song database to path, verifying it before it replaces
// anything already there.
//
// The download is written to a temporary file and only moved into place once
// SQLite's integrity check passes, so a failed or corrupt transfer never leaves
// a broken catalogue behind. That matters more than it sounds: a misassembled
// database can open fine and list every song, and only fail on one table.
func Fetch(ctx context.Context, dev Downloader, path string, progress Progress) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("preparing %s: %w", filepath.Dir(path), err)
	}

	r := NewReassembler()
	chunks, err := dev.DownloadDatabase(ctx, func(chunk []byte) error {
		if err := r.Add(chunk); err != nil {
			return err
		}
		have, total := r.Size()
		if progress != nil {
			progress(have, total)
		}
		// The device sends no terminator, so without this the transfer would
		// wait out client.DownloadOptions.Idle after the last chunk.
		if total > 0 && have == total {
			return client.ErrDownloadComplete
		}
		return nil
	}, client.DownloadOptions{})
	if err != nil {
		return fmt.Errorf("downloading the database: %w", err)
	}

	data, err := r.Finish()
	if err != nil {
		return fmt.Errorf("reassembling %d chunks: %w", chunks, err)
	}

	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := Verify(tmp); err != nil {
		// Keep the bad file for inspection rather than deleting the evidence.
		return fmt.Errorf("downloaded database failed verification (left at %s): %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("installing %s: %w", path, err)
	}
	return nil
}

// Ensure opens the database at path, downloading it first if it is missing or
// unusable. Returns the open database and whether a download happened.
func Ensure(ctx context.Context, dev Downloader, path string, progress Progress) (*DB, bool, error) {
	if db, err := Open(path); err == nil {
		return db, false, nil
	}
	if dev == nil {
		return nil, false, fmt.Errorf("%w at %s and no device to fetch it from", ErrNoDatabase, path)
	}
	if err := Fetch(ctx, dev, path, progress); err != nil {
		return nil, false, err
	}
	db, err := Open(path)
	if err != nil {
		return nil, true, err
	}
	return db, true, nil
}
