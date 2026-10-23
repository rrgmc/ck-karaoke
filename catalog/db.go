package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB is a read-only view of the device's song catalogue.
type DB struct {
	sql  *sql.DB
	path string
	info Info
}

// Info describes the catalogue.
type Info struct {
	ProjectName string
	ProductID   int
	DBVersion   int
	Songs       int
	Singers     int
}

// Open opens a song database and checks it is usable. It fails rather than
// degrade: a catalogue that is subtly corrupt produces confusing search results
// much later.
func Open(path string) (*DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("song database %s: %w", path, err)
	}
	// immutable=1 tells SQLite the file will not change, which avoids any
	// journal or lock files appearing next to it.
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&immutable=1"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	db := &DB{sql: sqlDB, path: path}
	if err := db.load(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) load() error {
	ctx := context.Background()
	row := d.sql.QueryRowContext(ctx,
		`select ProjectName, ProductID, DBVersion from TableDBInfo limit 1`)
	if err := row.Scan(&d.info.ProjectName, &d.info.ProductID, &d.info.DBVersion); err != nil {
		return fmt.Errorf("reading TableDBInfo from %s: %w", d.path, err)
	}
	if err := d.sql.QueryRowContext(ctx, `select count(*) from TableSong`).Scan(&d.info.Songs); err != nil {
		return fmt.Errorf("counting songs in %s: %w", d.path, err)
	}
	if err := d.sql.QueryRowContext(ctx, `select count(*) from TableSinger`).Scan(&d.info.Singers); err != nil {
		return fmt.Errorf("counting singers in %s: %w", d.path, err)
	}
	if d.info.Songs == 0 {
		return fmt.Errorf("%s has no songs", d.path)
	}
	return nil
}

// Info returns the catalogue's metadata.
func (d *DB) Info() Info { return d.info }

// Path returns the file backing this database.
func (d *DB) Path() string { return d.path }

// Close releases the database.
func (d *DB) Close() error { return d.sql.Close() }

// Verify runs SQLite's integrity check over a file.
//
// Worth doing on a freshly downloaded database, and worth doing properly: a
// misassembled file can open cleanly and list every song, and only fail when
// TableSongSinger is touched. Counting rows is not enough to catch that;
// integrity_check is.
func Verify(path string) error {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&immutable=1"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer sqlDB.Close()

	rows, err := sqlDB.Query(`pragma integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity check on %s: %w", path, err)
	}
	defer rows.Close()

	var problems []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		if s != "ok" {
			problems = append(problems, s)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(problems) > 0 {
		shown := problems
		if len(shown) > 3 {
			shown = shown[:3]
		}
		return fmt.Errorf("%s is corrupt: %v (and %d more)", path, shown, len(problems)-len(shown))
	}

	// integrity_check does not read every table's contents, so touch the join
	// table, which a misassembled file damages first.
	var n int
	if err := sqlDB.QueryRow(`select count(*) from TableSongSinger`).Scan(&n); err != nil {
		return fmt.Errorf("%s: TableSongSinger unreadable: %w", path, err)
	}
	return nil
}

// ErrNoDatabase reports that no catalogue is available yet.
var ErrNoDatabase = errors.New("no song database")
