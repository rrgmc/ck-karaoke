package webui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Favorites carries a class of its own, so it can look different from the two
// catalogue lists it shares a layout with.
func TestBrowseBlockNamesItsMode(t *testing.T) {
	s := syncServer(t)
	for _, mode := range []string{modeSongs, modeArtists, modeFavorites} {
		out := render(t, s, "_browsepage.html", pageData{Browse: browseData{Mode: mode}})
		assert.Contains(t, out, `<div id="browse" class="browse-`+mode+`">`)
	}
}

// Folder rows carry a folder icon; artist rows, the same shape otherwise, do not.
func TestFolderRowsHaveAnIcon(t *testing.T) {
	s := syncServer(t)
	defaultFolder(t, s)
	folders, err := s.favs.Folders(t.Context())
	require.NoError(t, err)

	out := render(t, s, "_browsepage.html", pageData{Browse: browseData{Mode: modeFavorites, Folders: folders}})
	assert.Contains(t, out, `class="folder-icon"`)
}

// Rename is a button right after Sync, in the control row, and the panel it
// opens is outside the search form, which cannot hold another form.
func TestRenameButtonFollowsSync(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)
	out := render(t, s, "_browsepage.html", pageData{Browse: browseData{
		Mode: modeFavorites, FolderID: f.ID, Folder: f,
	}})

	row := strings.Index(out, `class="filter-row"`)
	require.GreaterOrEqual(t, row, 0)
	closeForm := strings.Index(out[row:], "</form>") + row
	sync := strings.Index(out, "sync-btn")
	button := strings.Index(out, `for="renamefolder"`)
	panel := strings.Index(out, "rename-form")

	require.GreaterOrEqual(t, sync, 0, "no Sync button")
	require.GreaterOrEqual(t, button, 0, "no Rename button")
	assert.Less(t, sync, button, "Rename comes before Sync")
	assert.Less(t, button, closeForm, "Rename is not in the control row")
	assert.Greater(t, panel, closeForm, "the rename panel is nested inside the search form")
	assert.NotContains(t, out, "<details", "the old folded-away Rename link is still there")
}
