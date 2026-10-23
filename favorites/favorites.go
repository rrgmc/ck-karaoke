package favorites

import (
	"context"
	"fmt"
	"strings"
)

// Toggle files a song into a folder, or takes it out again if it is already
// there, and reports which way it went.
//
// Reporting the direction is what lets a handler word its response without
// asking again, and asking again would be a second round trip that could
// disagree with the write if another phone acted in between.
func (d *DB) Toggle(ctx context.Context, folderID, songID int) (added bool, err error) {
	res, err := d.sql.ExecContext(ctx,
		`delete from favorite where folder_id = ? and song_id = ?`, folderID, songID)
	if err != nil {
		return false, fmt.Errorf("removing song %d from folder %d: %w", songID, folderID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil // it was there; now it is not
	}

	if _, err := d.sql.ExecContext(ctx,
		`insert into favorite (folder_id, song_id) values (?, ?)`, folderID, songID); err != nil {
		if isForeignKeyViolation(err) {
			return false, ErrNoFolder
		}
		return false, fmt.Errorf("adding song %d to folder %d: %w", songID, folderID, err)
	}
	return true, nil
}

// AddSongs files songs into a folder, leaving alone any already there, and
// reports how many were new.
//
// This is the only write that merging favorites from another device performs,
// and it cannot remove anything. That is the guarantee the feature rests on,
// so it is expressed as the shape of the API rather than as a promise in a
// comment: importing the same code twice is not a special case that has been
// handled, it is simply the same insert a second time, which the
// (folder_id, song_id) primary key makes a no-op.
func (d *DB) AddSongs(ctx context.Context, folderID int, songIDs []int) (added int, err error) {
	if len(songIDs) == 0 {
		return 0, nil
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("adding songs to folder %d: %w", folderID, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// Checked up front so a folder deleted on another phone mid-merge reports
	// itself plainly. SQLite's OR IGNORE covers unique and check constraints but
	// explicitly not foreign keys, so this would otherwise surface as a driver
	// error message rather than ErrNoFolder.
	var n int
	if err := tx.QueryRowContext(ctx,
		`select count(*) from folder where id = ?`, folderID).Scan(&n); err != nil {
		return 0, fmt.Errorf("looking up folder %d: %w", folderID, err)
	}
	if n == 0 {
		return 0, ErrNoFolder
	}

	stmt, err := tx.PrepareContext(ctx,
		`insert or ignore into favorite (folder_id, song_id) values (?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("adding songs to folder %d: %w", folderID, err)
	}
	defer stmt.Close()

	for _, id := range songIDs {
		res, err := stmt.ExecContext(ctx, folderID, id)
		if err != nil {
			return 0, fmt.Errorf("adding song %d to folder %d: %w", id, folderID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		added += int(n) // 0 when it was already filed here
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("adding songs to folder %d: %w", folderID, err)
	}
	return added, nil
}

// RemoveSong takes one song out of one folder, and is content if it was not
// there.
//
// Sync never calls this — merging only ever adds, and AddSongs is the whole of
// what it can do — but someone filing songs by hand has to be able to take one
// back out again.
func (d *DB) RemoveSong(ctx context.Context, folderID, songID int) error {
	if _, err := d.sql.ExecContext(ctx,
		`delete from favorite where folder_id = ? and song_id = ?`, folderID, songID); err != nil {
		return fmt.Errorf("removing song %d from folder %d: %w", songID, folderID, err)
	}
	return nil
}

// SongIDs returns the songs filed in a folder, most recently added first.
func (d *DB) SongIDs(ctx context.Context, folderID int) ([]int, error) {
	rows, err := d.sql.QueryContext(ctx,
		`select song_id from favorite where folder_id = ? order by added_at desc, song_id`, folderID)
	if err != nil {
		return nil, fmt.Errorf("listing folder %d: %w", folderID, err)
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// FoldersForSong reports which folders hold a song, as a set of folder ids.
func (d *DB) FoldersForSong(ctx context.Context, songID int) (map[int]bool, error) {
	rows, err := d.sql.QueryContext(ctx,
		`select folder_id from favorite where song_id = ?`, songID)
	if err != nil {
		return nil, fmt.Errorf("looking up folders for song %d: %w", songID, err)
	}
	defer rows.Close()

	out := map[int]bool{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// FavoritedIDs reports which of the given songs are in at least one folder.
//
// One query for a whole page of results: asking per row would be fifty queries
// to draw fifty stars.
func (d *DB) FavoritedIDs(ctx context.Context, songIDs []int) (map[int]bool, error) {
	out := map[int]bool{}
	if len(songIDs) == 0 {
		return out, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(songIDs)), ",")
	args := make([]any, len(songIDs))
	for i, id := range songIDs {
		args[i] = id
	}

	rows, err := d.sql.QueryContext(ctx,
		`select distinct song_id from favorite where song_id in (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("looking up favorites: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY CONSTRAINT FAILED")
}
