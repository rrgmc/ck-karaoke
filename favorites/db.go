// Package favorites stores favorite songs, organised into folders.
//
// It is deliberately a separate database from the song catalogue. catalog opens
// the catalogue read-only and immutable because it is a device artifact, and
// ck-karaoke-server deletes that file outright on -refresh-db — so anything the user
// authored would not survive a routine catalogue refresh.
//
// Only song ids are kept here. Titles are joined from the catalogue when
// needed, the same way the reserved list already works.
package favorites

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DefaultFolder is created when the database is new, so there is somewhere to
// put a song before anyone has thought about folders.
const DefaultFolder = "Favorites"

// DB holds folders and the songs filed in them.
type DB struct {
	sql  *sql.DB
	path string
}

// Folder is a named collection of songs.
type Folder struct {
	ID    int
	Name  string
	Songs int
}

// Open opens the favorites database, creating it if it does not exist.
//
// WAL and a busy timeout are what make this safe for several phones at once:
// every client shares one server, but writes still serialise, and without a
// timeout a concurrent write fails immediately rather than waiting its turn.
func Open(path string) (*DB, error) {
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(on)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	db := &DB{sql: sqlDB, path: path}
	if err := db.migrate(context.Background()); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) migrate(ctx context.Context) error {
	const schema = `
create table if not exists folder (
	id         integer primary key autoincrement,
	name       text not null collate nocase unique,
	created_at text not null default (datetime('now'))
);
create table if not exists favorite (
	folder_id integer not null references folder(id) on delete cascade,
	song_id   integer not null,
	added_at  text not null default (datetime('now')),
	primary key (folder_id, song_id)
);
create index if not exists favorite_song on favorite(song_id);`

	if _, err := d.sql.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating schema in %s: %w", d.path, err)
	}

	// Seed only when no folder has ever existed. Checking for the *name* would
	// recreate it for anyone who renamed it, which is worse than having no
	// default: it would come back every restart.
	var n int
	if err := d.sql.QueryRowContext(ctx, `select count(*) from folder`).Scan(&n); err != nil {
		return fmt.Errorf("counting folders in %s: %w", d.path, err)
	}
	if n == 0 {
		if _, err := d.sql.ExecContext(ctx,
			`insert into folder (name) values (?)`, DefaultFolder); err != nil {
			return fmt.Errorf("creating the %q folder: %w", DefaultFolder, err)
		}
	}
	return nil
}

// Path returns the file backing this database.
func (d *DB) Path() string { return d.path }

// Close releases the database.
func (d *DB) Close() error { return d.sql.Close() }
