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
	{ErrNoDevice, "No karaoke device has been found yet."},
	{ErrNoQueueManager, "No queue manager has been found yet."},
	{client.ErrBusy, "The device is not accepting connections."},
	{client.ErrNotPlaying, "The queue can be reordered only while a song plays."},
	{ErrQueueManager, "The queue manager is not answering."},
	{ErrAway, "The karaoke machine is not connected to the queue manager."},
	{ErrNeedsCode, "This needs an access code. Enter one on the Setup page."},
	{ErrNameRequired, "Give your name on My songs first."},
	{ErrRequestsClosed, "The host has paused requests for now."},
	{ErrWrongCode, "That code does not open anything. Check it and try again."},
	{ErrUnsupported, "This is not available here."},
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
	{errNothingIncoming, "Nothing was received, or it was already added."},
	{errSongListDownload, "The song list could not be downloaded. It will be tried again on the next connection."},
	{errSongListTooLarge, "That file is too large to be a song list."},
	{errNotSongList, "That file is not a song list from a karaoke device."},
	{errNoFile, "No file was chosen."},
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
