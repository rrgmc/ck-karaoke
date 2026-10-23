// Asks the hx-confirm questions in the page instead of through confirm().
//
// WKWebView shows a JavaScript confirm() only when the app implements the
// matching WKUIDelegate method, and answers Cancel otherwise, without showing
// anything. The iOS app and Wails on macOS do not implement it, so every
// hx-confirm button did nothing there. htmx raises htmx:confirm before it asks;
// cancelling that event and calling issueRequest(true) later replaces the
// browser's dialog with this one, on every platform alike.
(function () {
  "use strict";

  var box = document.getElementById("confirm");
  var text = document.getElementById("confirm-text");
  if (!box || !text) return;

  // The htmx:confirm detail of the question on screen, or null.
  var pending = null;

  function close(yes) {
    var detail = pending;
    pending = null;
    box.hidden = true;
    if (detail && yes) detail.issueRequest(true);
  }

  document.body.addEventListener("htmx:confirm", function (evt) {
    if (!evt.detail.question) return;
    evt.preventDefault();
    pending = evt.detail;
    text.textContent = evt.detail.question;
    box.hidden = false;
    // Cancel has the focus, so a stray Enter does not remove anything.
    box.querySelector('[data-confirm="no"]').focus();
  });

  box.addEventListener("click", function (evt) {
    var button = evt.target.closest("[data-confirm]");
    if (button) close(button.getAttribute("data-confirm") === "yes");
  });

  document.addEventListener("keydown", function (evt) {
    if (evt.key === "Escape" && pending) close(false);
  });
})();
