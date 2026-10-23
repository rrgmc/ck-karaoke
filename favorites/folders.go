package favorites

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Errors callers are expected to handle rather than merely report.
var (
	// ErrNoName is a folder name that is blank once trimmed.
	ErrNoName = errors.New("a folder needs a name")
	// ErrDuplicateName is a name another folder already uses, compared without
	// regard to case.
	ErrDuplicateName = errors.New("a folder with that name already exists")
	// ErrLastFolder refuses to remove the only folder left. Without it the
	// picker would have nothing to offer and no way to get a folder back.
	ErrLastFolder = errors.New("this is the only folder left, so it cannot be deleted")
	// ErrNoFolder is an id that does not exist.
	ErrNoFolder = errors.New("no such folder")
)

// Folders lists every folder with the number of songs filed in it.
//
// The count is memberships rather than distinct songs, which is the same thing
// per folder — a song can be in several folders, so the counts can sum to more
// than the number of favorited songs.
func (d *DB) Folders(ctx context.Context) ([]Folder, error) {
	rows, err := d.sql.QueryContext(ctx, `
		select f.id, f.name, count(v.song_id)
		from folder f
		left join favorite v on v.folder_id = f.id
		group by f.id, f.name
		order by f.name collate nocase`)
	if err != nil {
		return nil, fmt.Errorf("listing folders: %w", err)
	}
	defer rows.Close()

	var out []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Name, &f.Songs); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Folder returns one folder by id.
func (d *DB) Folder(ctx context.Context, id int) (Folder, error) {
	var f Folder
	err := d.sql.QueryRowContext(ctx, `
		select f.id, f.name, count(v.song_id)
		from folder f
		left join favorite v on v.folder_id = f.id
		where f.id = ?
		group by f.id, f.name`, id).Scan(&f.ID, &f.Name, &f.Songs)
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, ErrNoFolder
	}
	if err != nil {
		return Folder{}, fmt.Errorf("reading folder %d: %w", id, err)
	}
	return f, nil
}

// CreateFolder adds a folder. The name is trimmed, must not be blank, and must
// not collide with an existing one.
func (d *DB) CreateFolder(ctx context.Context, name string) (Folder, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Folder{}, ErrNoName
	}

	res, err := d.sql.ExecContext(ctx, `insert into folder (name) values (?)`, name)
	if err != nil {
		if isUniqueViolation(err) {
			return Folder{}, ErrDuplicateName
		}
		return Folder{}, fmt.Errorf("creating folder %q: %w", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Folder{}, err
	}
	return Folder{ID: int(id), Name: name}, nil
}

// EnsureFolder returns the folder with this name, creating it if there is none,
// and reports whether it had to create one.
//
// Written for restoring a backup, and written in the same spirit as AddSongs:
// the worst it can do is nothing. It cannot rename, cannot delete and cannot
// move a song — a restore that meets a folder it recognises simply files into
// it. CreateFolder alone is not enough for that, because it reports
// ErrDuplicateName without saying which folder it collided with.
//
// The name is matched by the `collate nocase` unique constraint on the column
// rather than by lowercasing here, so this agrees with what an insert would do
// by construction.
func (d *DB) EnsureFolder(ctx context.Context, name string) (folder Folder, created bool, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Folder{}, false, ErrNoName
	}

	// Asked first, because the common case on a restore is a folder that is
	// already here and an insert would only fail.
	if f, err := d.folderNamed(ctx, name); err == nil {
		return f, false, nil
	} else if !errors.Is(err, ErrNoFolder) {
		return Folder{}, false, err
	}

	f, err := d.CreateFolder(ctx, name)
	switch {
	case errors.Is(err, ErrDuplicateName):
		// Another phone created it between the two statements. That is the
		// outcome this wanted, so it is not an error — a restore is add-only and
		// has nothing to lose to a race it has already won.
		f, err = d.folderNamed(ctx, name)
		return f, false, err
	case err != nil:
		return Folder{}, false, err
	}
	return f, true, nil
}

// folderNamed looks a folder up by name, case-insensitively.
func (d *DB) folderNamed(ctx context.Context, name string) (Folder, error) {
	var f Folder
	err := d.sql.QueryRowContext(ctx, `
		select f.id, f.name, count(v.song_id)
		from folder f
		left join favorite v on v.folder_id = f.id
		where f.name = ? collate nocase
		group by f.id, f.name`, name).Scan(&f.ID, &f.Name, &f.Songs)
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, ErrNoFolder
	}
	if err != nil {
		return Folder{}, fmt.Errorf("looking up folder %q: %w", name, err)
	}
	return f, nil
}

// RenameFolder changes a folder's name, under the same rules as creating one.
func (d *DB) RenameFolder(ctx context.Context, id int, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrNoName
	}

	res, err := d.sql.ExecContext(ctx, `update folder set name = ? where id = ?`, name, id)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateName
		}
		return fmt.Errorf("renaming folder %d: %w", id, err)
	}
	return mustAffect(res, ErrNoFolder)
}

// DeleteFolder removes a folder and, by cascade, the memberships filed in it.
// Songs in other folders are untouched.
func (d *DB) DeleteFolder(ctx context.Context, id int) error {
	var n int
	if err := d.sql.QueryRowContext(ctx, `select count(*) from folder`).Scan(&n); err != nil {
		return fmt.Errorf("counting folders: %w", err)
	}
	if n <= 1 {
		return ErrLastFolder
	}

	res, err := d.sql.ExecContext(ctx, `delete from folder where id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting folder %d: %w", id, err)
	}
	return mustAffect(res, ErrNoFolder)
}

func mustAffect(res sql.Result, missing error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return missing
	}
	return nil
}

// isUniqueViolation reports whether an error is SQLite's unique-constraint
// failure. modernc's driver does not export a typed error for it, so the
// message is what there is to go on.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}
