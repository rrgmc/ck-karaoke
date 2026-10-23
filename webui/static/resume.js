// Re-opens the event stream after the app has been suspended, without reloading
// the page.
//
// Only the iOS wrapper calls this, and only when its grace period ran out and the
// device session was actually given up. It exists because the alternative was a
// full reload, and a reload costs far more than it looks: the song list is paged
// at fifty rows with a Load more button that appends into the DOM, and the ⋯
// toggle sets hx-push-url="false". None of that is in the
// URL, so a reload cannot bring any of it back — it returns you to the first
// fifty rows of an unfiltered list, which is what "it lost my place" turned out
// to mean. Nothing the stream feeds is the list, either: sse-swap drives the
// toasts, the banner, the connection dot, the queue count, the queue and the
// player, and every one of those is a small targeted swap. Rebuilding the whole
// document to refresh them is the wrong trade.
//
// The stream itself genuinely does have to be rebuilt. It does not survive being
// frozen: the server goes on writing to a subscriber it still believes in, the
// page applies none of it and — because nothing errors — never reconnects
// either, which is how the offline banner came to outlive the reconnection it
// was reporting.
(function () {
  "use strict";

  // htmx-sse.js hoists its EventSource constructor onto the public API expressly
  // so it can be replaced, and wrapping it is the only supported way to hold a
  // reference to the sources it opens: the extension keeps them in htmx internal
  // data, which no page script can reach. This runs after htmx-sse.js and before
  // the body is processed — defineExtension calls init synchronously, so the
  // property is already there, and every source the extension ever opens goes
  // through here.
  if (!window.htmx || typeof htmx.createEventSource !== "function") return;

  var opened = [];
  var create = htmx.createEventSource;
  htmx.createEventSource = function (url) {
    var source = create(url);
    opened.push(source);
    return source;
  };

  // Returns the number of streams it acted on, so the caller can tell "rebuilt
  // it" from "there was nothing here" and fall back to a reload for the latter.
  window.ckResume = function () {
    var stale = opened;
    opened = [];

    stale.forEach(function (source) {
      var onerror = source.onerror;
      if (source.readyState !== EventSource.CLOSED) source.close();
      // Handing the extension its own error is deliberate, and is not a trick to
      // route around it. Its onerror is where reconnection lives, and it acts
      // only on a source that is already CLOSED — which close() makes true and
      // does not announce, since a manual close fires no event. Going through it
      // rather than building a source here is what re-registers every sse-swap
      // element against the new connection: the extension does that on open, but
      // only when it believes it is retrying. Opening one by hand reconnects the
      // stream and silently leaves every swap target listening to the dead one.
      if (typeof onerror === "function") onerror(new Event("error"));
    });

    return stale.length;
  };
})();
