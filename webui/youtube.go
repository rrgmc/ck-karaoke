package webui

import (
	"net/url"
	"strings"
)

// youtubeSearch is the helper behind {{ytSearch .Artist .Title}}: a YouTube
// search for one song, as an ordinary https URL.
//
// Always https://www.youtube.com/…, never a youtube:// scheme. One page serves
// all three front ends, and both app wrappers hand any host that is not
// loopback to the system rather than loading it — so https reaches the YouTube
// app through its universal links on iOS and its intent filter on Android, and
// a desktop browser gets something it can simply open. A custom scheme would
// reach none of those, and on iOS it would be actively harmful: WKWebView
// cannot load it, so the navigation fails, and a failed navigation is exactly
// how that app decides the local server has died — it stops and restarts it.
// See RootViewController.showLoadFailure.
//
// The artist can be empty: some catalogue rows carry no performer, and
// searching " - SOME TITLE" is a worse query than the title on its own, so the
// separator is written only when there is something to its left. The artist is
// also truncated to 24 characters by the device — "Zezé Di Camargo e Lucian" —
// which YouTube copes with and this does not try to repair.
//
// The result is a whole href and must never be spliced into an existing query
// string. In href="{{ytSearch …}}" html/template applies urlFilter then
// urlNormalizer, which leaves valid %XX alone; after a ? it would apply
// urlEscaper instead, which re-encodes % and would turn every escape into
// %25XX. Returning a plain string rather than a template.URL keeps urlFilter in
// that pipeline, so a scheme other than http(s) would be defanged rather than
// quietly shipped.
func youtubeSearch(artist, title string) string {
	q := strings.TrimSpace(title)
	if a := strings.TrimSpace(artist); a != "" {
		q = a + " - " + q
	}
	return "https://www.youtube.com/results?search_query=" + url.QueryEscape(q)
}
