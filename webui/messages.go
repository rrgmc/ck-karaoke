package webui

import (
	"errors"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/favorites"
	"github.com/rrgmc/ck-karaoke/favorites/qrshare"
	"github.com/rrgmc/ck-karaoke/favorites/xmlbackup"
	"github.com/rrgmc/ck-karaoke/i18n"
)

// knownErrors pairs the library errors a user can meet with what to tell them.
// The library words its errors in English for logs and callers; this is where
// they become the page's language.
var knownErrors = []struct {
	err error
	msg string
}{
	{ErrOffline, "The device is offline."},
	{client.ErrBusy, "The device is not accepting connections."},
	{favorites.ErrNoName, "A folder needs a name."},
	{favorites.ErrDuplicateName, "A folder with that name already exists."},
	{favorites.ErrLastFolder, "This is the only folder left, so it cannot be deleted."},
	{favorites.ErrNoFolder, "That folder no longer exists."},
	{qrshare.ErrFormat, "This does not look like a favorites code."},
	{qrshare.ErrChecksum, "This code is damaged. Scan or copy it again."},
	{qrshare.ErrEmpty, "There are no songs to share."},
	{qrshare.ErrTooLarge, "This folder is too large to share as one code."},
	{xmlbackup.ErrFormat, "This does not look like a favorites file."},
	{xmlbackup.ErrVersion, "This file was made by a newer version of the app."},
	{xmlbackup.ErrEmpty, "There are no songs in this file."},
}

// errorText words an error for the page. A known error gets its translated
// message; anything else is shown as it is, since it is more use to someone
// reporting a problem than a vague translation would be.
func errorText(lang i18n.Lang, err error) string {
	for _, k := range knownErrors {
		if errors.Is(err, k.err) {
			return lang.T(k.msg)
		}
	}
	return err.Error()
}
