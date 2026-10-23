package favorites

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func open(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "favorites.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func TestNewDatabaseHasOneDefaultFolder(t *testing.T) {
	db, _ := open(t)

	folders, err := db.Folders(context.Background())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("got %d folders, want 1", len(folders))
	}
	if folders[0].Name != DefaultFolder {
		t.Errorf("folder is %q, want %q", folders[0].Name, DefaultFolder)
	}
	if folders[0].Songs != 0 {
		t.Errorf("new folder holds %d songs, want 0", folders[0].Songs)
	}
}

// The whole point of a separate file is that it outlives the catalogue, so
// reopening must find everything — and must not helpfully recreate a default
// folder the user has renamed.
func TestReopenPreservesEverythingAndDoesNotReseed(t *testing.T) {
	db, path := open(t)
	ctx := context.Background()

	folders, _ := db.Folders(ctx)
	if err := db.RenameFolder(ctx, folders[0].ID, "Sertanejo"); err != nil {
		t.Fatalf("renaming: %v", err)
	}
	if _, err := db.Toggle(ctx, folders[0].ID, 2511); err != nil {
		t.Fatalf("adding: %v", err)
	}
	db.Close()

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer again.Close()

	got, err := again.Folders(ctx)
	if err != nil {
		t.Fatalf("listing after reopen: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d folders after reopen, want 1 — %q was recreated", len(got), DefaultFolder)
	}
	if got[0].Name != "Sertanejo" {
		t.Errorf("folder is %q, want the renamed %q", got[0].Name, "Sertanejo")
	}
	if got[0].Songs != 1 {
		t.Errorf("folder holds %d songs after reopen, want 1", got[0].Songs)
	}
}

func TestSongCanBeInSeveralFolders(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()

	rock, err := db.CreateFolder(ctx, "Rock")
	if err != nil {
		t.Fatalf("creating: %v", err)
	}
	folders, _ := db.Folders(ctx)
	var def Folder
	for _, f := range folders {
		if f.Name == DefaultFolder {
			def = f
		}
	}

	for _, id := range []int{def.ID, rock.ID} {
		added, err := db.Toggle(ctx, id, 2511)
		if err != nil {
			t.Fatalf("filing into %d: %v", id, err)
		}
		if !added {
			t.Errorf("filing into %d reported a removal", id)
		}
	}

	in, err := db.FoldersForSong(ctx, 2511)
	if err != nil {
		t.Fatalf("folders for song: %v", err)
	}
	if !in[def.ID] || !in[rock.ID] {
		t.Errorf("song is in %v, want both %d and %d", in, def.ID, rock.ID)
	}
}

// Deleting one folder must not disturb the same song's place in another.
func TestDeletingAFolderLeavesOtherMembershipsAlone(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()

	keep, _ := db.CreateFolder(ctx, "Keep")
	drop, _ := db.CreateFolder(ctx, "Drop")
	db.Toggle(ctx, keep.ID, 2511)
	db.Toggle(ctx, drop.ID, 2511)
	db.Toggle(ctx, drop.ID, 5853)

	if err := db.DeleteFolder(ctx, drop.ID); err != nil {
		t.Fatalf("deleting: %v", err)
	}

	fav, err := db.FavoritedIDs(ctx, []int{2511, 5853})
	if err != nil {
		t.Fatalf("favorited: %v", err)
	}
	if !fav[2511] {
		t.Error("2511 stopped being a favorite, but it is still in Keep")
	}
	if fav[5853] {
		t.Error("5853 is still a favorite, but its only folder was deleted")
	}
}

func TestLastFolderCannotBeDeleted(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()

	folders, _ := db.Folders(ctx)
	if err := db.DeleteFolder(ctx, folders[0].ID); !errors.Is(err, ErrLastFolder) {
		t.Fatalf("deleting the only folder: %v, want ErrLastFolder", err)
	}

	extra, _ := db.CreateFolder(ctx, "Second")
	if err := db.DeleteFolder(ctx, extra.ID); err != nil {
		t.Errorf("deleting one of two: %v, want success", err)
	}
}

func TestToggleReportsDirectionAndIsIdempotent(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()
	folders, _ := db.Folders(ctx)
	id := folders[0].ID

	if added, _ := db.Toggle(ctx, id, 2511); !added {
		t.Error("first toggle reported a removal")
	}
	if added, _ := db.Toggle(ctx, id, 2511); added {
		t.Error("second toggle reported an addition")
	}
	if added, _ := db.Toggle(ctx, id, 2511); !added {
		t.Error("third toggle reported a removal")
	}

	ids, _ := db.SongIDs(ctx, id)
	if len(ids) != 1 || ids[0] != 2511 {
		t.Errorf("folder holds %v, want exactly [2511]", ids)
	}
}

func TestFolderNames(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()

	if _, err := db.CreateFolder(ctx, "   "); !errors.Is(err, ErrNoName) {
		t.Errorf("blank name: %v, want ErrNoName", err)
	}
	if _, err := db.CreateFolder(ctx, "Rock"); err != nil {
		t.Fatalf("creating Rock: %v", err)
	}
	if _, err := db.CreateFolder(ctx, "rock"); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("duplicate differing only in case: %v, want ErrDuplicateName", err)
	}

	f, err := db.CreateFolder(ctx, "  Padded  ")
	if err != nil {
		t.Fatalf("creating padded: %v", err)
	}
	if f.Name != "Padded" {
		t.Errorf("name is %q, want it trimmed", f.Name)
	}
}

func TestMissingFolder(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()

	if _, err := db.Folder(ctx, 999); !errors.Is(err, ErrNoFolder) {
		t.Errorf("reading a missing folder: %v, want ErrNoFolder", err)
	}
	if err := db.RenameFolder(ctx, 999, "x"); !errors.Is(err, ErrNoFolder) {
		t.Errorf("renaming a missing folder: %v, want ErrNoFolder", err)
	}
	if _, err := db.Toggle(ctx, 999, 2511); !errors.Is(err, ErrNoFolder) {
		t.Errorf("filing into a missing folder: %v, want ErrNoFolder", err)
	}
}

// Merging favorites from another device is a union, and this is the property
// the whole feature rests on: doing it twice is doing it once.
func TestAddSongsIsIdempotent(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()
	folder := folderID(t, db, DefaultFolder)

	ids := []int{101, 202, 303}
	added, err := db.AddSongs(ctx, folder, ids)
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if added != 3 {
		t.Errorf("first merge added %d, want 3", added)
	}

	added, err = db.AddSongs(ctx, folder, ids)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if added != 0 {
		t.Errorf("second merge added %d, want 0", added)
	}

	got, err := db.SongIDs(ctx, folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("folder holds %d songs after merging twice, want 3", len(got))
	}
}

// Only what is missing lands, and what was already there is left alone —
// including its place in the "recently added" order.
func TestAddSongsOnlyAddsWhatIsMissing(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()
	folder := folderID(t, db, DefaultFolder)

	if _, err := db.AddSongs(ctx, folder, []int{1, 2}); err != nil {
		t.Fatal(err)
	}
	added, err := db.AddSongs(ctx, folder, []int{2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Errorf("added %d, want 2 (3 and 4 only)", added)
	}

	got, err := db.SongIDs(ctx, folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("folder holds %v, want four songs", got)
	}
}

// The guarantee in one test: whatever a merge is handed, the folder never
// shrinks. A regression that turned the insert into a replace would pass every
// count test above and fail this one.
func TestAddSongsNeverRemovesAnything(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()
	folder := folderID(t, db, DefaultFolder)

	if _, err := db.AddSongs(ctx, folder, []int{10, 20, 30}); err != nil {
		t.Fatal(err)
	}
	for _, merge := range [][]int{{40}, {10}, {}, {50, 20}, {10, 20, 30}} {
		before, err := db.SongIDs(ctx, folder)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.AddSongs(ctx, folder, merge); err != nil {
			t.Fatalf("merging %v: %v", merge, err)
		}
		after, err := db.SongIDs(ctx, folder)
		if err != nil {
			t.Fatal(err)
		}
		if len(after) < len(before) {
			t.Fatalf("merging %v shrank the folder from %d to %d", merge, len(before), len(after))
		}
		for _, id := range before {
			if !slices.Contains(after, id) {
				t.Fatalf("merging %v lost song %d", merge, id)
			}
		}
	}
}

// A folder deleted on another phone mid-merge must say so plainly.
func TestAddSongsToMissingFolder(t *testing.T) {
	db, _ := open(t)
	if _, err := db.AddSongs(context.Background(), 9999, []int{1}); !errors.Is(err, ErrNoFolder) {
		t.Errorf("got %v, want ErrNoFolder", err)
	}
}

// A merge into one folder must not touch another, since a song can be filed in
// several and they are independent.
func TestAddSongsLeavesOtherFoldersAlone(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()

	rock, err := db.CreateFolder(ctx, "Rock")
	if err != nil {
		t.Fatal(err)
	}
	favs := folderID(t, db, DefaultFolder)

	if _, err := db.AddSongs(ctx, favs, []int{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddSongs(ctx, rock.ID, []int{9}); err != nil {
		t.Fatal(err)
	}

	got, err := db.SongIDs(ctx, favs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("the default folder holds %v after a merge into Rock, want three songs", got)
	}
}

func folderID(t *testing.T, db *DB, name string) int {
	t.Helper()
	folders, err := db.Folders(context.Background())
	if err != nil {
		t.Fatalf("listing folders: %v", err)
	}
	for _, f := range folders {
		if f.Name == name {
			return f.ID
		}
	}
	t.Fatalf("no folder named %q", name)
	return 0
}

// Sync only ever adds, but a person filing by hand has to be able to take a
// song back out.
func TestRemoveSong(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()
	folder := folderID(t, db, DefaultFolder)

	if _, err := db.AddSongs(ctx, folder, []int{11, 22, 33}); err != nil {
		t.Fatal(err)
	}
	if err := db.RemoveSong(ctx, folder, 22); err != nil {
		t.Fatalf("removing: %v", err)
	}

	got, err := db.SongIDs(ctx, folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || slices.Contains(got, 22) {
		t.Errorf("folder holds %v, want 11 and 33", got)
	}

	// Removing what is not there is not an error: two phones can race, and the
	// second one to ask has nothing left to do.
	if err := db.RemoveSong(ctx, folder, 22); err != nil {
		t.Errorf("removing a song that had gone: %v", err)
	}
}

// One folder's removal must not reach into another holding the same song.
func TestRemoveSongLeavesOtherFoldersAlone(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()
	rock, err := db.CreateFolder(ctx, "Rock")
	if err != nil {
		t.Fatal(err)
	}
	favs := folderID(t, db, DefaultFolder)

	for _, id := range []int{favs, rock.ID} {
		if _, err := db.AddSongs(ctx, id, []int{7}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RemoveSong(ctx, favs, 7); err != nil {
		t.Fatal(err)
	}

	got, err := db.SongIDs(ctx, rock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("Rock holds %v after a removal from another folder, want just 7", got)
	}
}
