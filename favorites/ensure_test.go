package favorites

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A separate file from favorites_test.go, which predates testify and is left as it
// is rather than converted wholesale.

func TestEnsureFolderCreatesOnceAndThenFindsIt(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()

	first, created, err := db.EnsureFolder(ctx, "Rock")
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "Rock", first.Name)

	again, created, err := db.EnsureFolder(ctx, "Rock")
	require.NoError(t, err)
	assert.False(t, created, "the second call must find the first one")
	assert.Equal(t, first.ID, again.ID)

	folders, err := db.Folders(ctx)
	require.NoError(t, err)
	assert.Len(t, folders, 2, "only the default folder and Rock")
}

// favorites' unique constraint is `collate nocase`, so an insert of "rock" beside
// "Rock" would fail. Matching the same way is what keeps a restore from turning
// a difference in capitalisation into an error.
func TestEnsureFolderMatchesWithoutCase(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()

	rock, err := db.CreateFolder(ctx, "Rock")
	require.NoError(t, err)

	got, created, err := db.EnsureFolder(ctx, "  rOcK  ")
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, rock.ID, got.ID)
	assert.Equal(t, "Rock", got.Name, "the name already here is the one that stands")
}

// It reports the count the same way Folder does, so a caller can tell what it
// found without a second query.
func TestEnsureFolderReportsTheCount(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()

	rock, err := db.CreateFolder(ctx, "Rock")
	require.NoError(t, err)
	_, err = db.AddSongs(ctx, rock.ID, []int{1, 2, 3})
	require.NoError(t, err)

	got, created, err := db.EnsureFolder(ctx, "Rock")
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, 3, got.Songs)
}

func TestEnsureFolderRefusesABlankName(t *testing.T) {
	db, _ := open(t)

	_, _, err := db.EnsureFolder(context.Background(), "   ")
	assert.ErrorIs(t, err, ErrNoName)
}

// It adds a folder and nothing else: an existing folder keeps its name and every
// song already filed in it.
func TestEnsureFolderTakesNothingAway(t *testing.T) {
	db, _ := open(t)
	ctx := context.Background()

	rock, err := db.CreateFolder(ctx, "Rock")
	require.NoError(t, err)
	_, err = db.AddSongs(ctx, rock.ID, []int{7, 8})
	require.NoError(t, err)

	_, _, err = db.EnsureFolder(ctx, "Rock")
	require.NoError(t, err)

	ids, err := db.SongIDs(ctx, rock.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int{7, 8}, ids)
}
