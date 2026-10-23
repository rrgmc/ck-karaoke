package webui

import (
	"fmt"
	"html/template"
	"net/http"
)

// Toast severities.
const (
	toastInfo = "info"
	toastGood = "good"
	toastWarn = "warn"
	toastBad  = "bad"
)

// toastHTML renders one notification.
//
// The element removes itself when its animation finishes, so notifications do
// not pile up invisibly in the DOM over a long session. That is the one place a
// line of inline script earns its keep — a CSS animation can hide a toast but
// cannot take it out of the document.
func toastHTML(level, message string) string {
	return fmt.Sprintf(`<div class="toast-item toast-%s" role="status" onanimationend="this.remove()">%s</div>`,
		template.HTMLEscapeString(level), template.HTMLEscapeString(message))
}

// oobToast wraps a toast so htmx grafts it onto the notification area of
// whatever page is open, regardless of what the response was actually for.
func oobToast(level, message string) string {
	return fmt.Sprintf(`<div id="toasts" hx-swap-oob="afterbegin">%s</div>`,
		toastHTML(level, message))
}

// notify sends a toast to every connected browser. Used for things nobody asked
// for, like the device dropping off.
func (s *Server) notify(level, message string) {
	s.hub.publish(eventToast, toastHTML(level, message))
}

// withToast appends an out-of-band toast to a response already being written.
// Used for the result of an action, so the notification lands on the page that
// triggered it even if the swap target is somewhere else entirely.
func withToast(w http.ResponseWriter, level, message string) {
	fmt.Fprint(w, oobToast(level, message))
}
