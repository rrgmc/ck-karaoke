// Reads a favorites code with the camera and submits it.
//
// The page carries its own decoder rather than using BarcodeDetector. That API
// is in Chrome and therefore in Android's WebView, but not in WebKit — so
// relying on it would leave the iPad without the half of this that matters
// most, since iOS's own camera app handles a plain-text QR poorly and that is
// the whole reason for scanning inside the app.
//
// The messages it shows come from data attributes on the #scan box, which the
// server renders in the page's language.
//
// Nothing here understands the code. It goes into a hidden field and straight
// back to the server, which is what names the folder and works out where the
// songs should go.
(function () {
  "use strict";

  var box = document.getElementById("scan");
  if (!box) return;

  var startBtn = document.getElementById("scan-start");
  var stopBtn = document.getElementById("scan-stop");
  var view = document.getElementById("scan-view");
  var video = document.getElementById("scan-video");
  var status = document.getElementById("scan-status");
  var form = document.getElementById("scan-form");
  var field = document.getElementById("scan-code");

  // No camera, no button. A control that cannot work is worse than none at
  // all here, because the typed fallback underneath still does the whole job.
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia || !window.jsQR) return;
  box.hidden = false;

  var canvas = document.createElement("canvas");
  var ctx = canvas.getContext("2d", { willReadFrequently: true });
  var stream = null;
  var timer = 0;
  var done = false;

  startBtn.addEventListener("click", start);
  stopBtn.addEventListener("click", function () {
    stop();
    say("");
  });
  // Navigating away with the camera still running would leave the light on.
  window.addEventListener("pagehide", stop);

  // Straight in: you are on a page called Receive, so the camera is what you
  // came for. Both app wrappers answer the permission natively and remember
  // it, so this does not put a prompt up every visit.
  start();

  function say(text) {
    status.textContent = text;
  }

  function start() {
    startBtn.hidden = true;
    view.hidden = false;
    stopBtn.hidden = false;
    say(box.dataset.starting);

    navigator.mediaDevices.getUserMedia({
      // The back camera, at enough resolution to resolve a dense code without
      // asking for more than a phone can decode several times a second.
      video: {
        facingMode: "environment",
        width: { ideal: 1280 },
        height: { ideal: 960 },
      },
      audio: false,
    }).then(function (s) {
      stream = s;
      video.srcObject = s;
      return video.play();
    }).then(function () {
      say("");
      tick();
    }).catch(function (err) {
      stop();
      say(explain(err));
    });
  }

  // Ten times a second rather than every frame: reading back a 1280×960 frame
  // and decoding it is the expensive part, and a code held up to a camera does
  // not need sixty attempts a second to be caught.
  function tick() {
    timer = window.setTimeout(tick, 100);
    if (video.readyState < 2 || !video.videoWidth) return;

    canvas.width = video.videoWidth;
    canvas.height = video.videoHeight;
    ctx.drawImage(video, 0, 0, canvas.width, canvas.height);

    var frame = ctx.getImageData(0, 0, canvas.width, canvas.height);
    // dontInvert: ours is always dark on light, and trying both ways doubles
    // the work on every attempt for a case that cannot arise.
    var found = window.jsQR(frame.data, frame.width, frame.height, {
      inversionAttempts: "dontInvert",
    });
    if (!found || !found.data || done) return;

    // Guarded, because stopping the camera is not instant and a second frame
    // could otherwise submit the form twice.
    done = true;
    field.value = found.data.trim();
    stop();
    if (navigator.vibrate) navigator.vibrate(60);
    say(box.dataset.got);
    form.submit();
  }

  function stop() {
    if (timer) {
      window.clearTimeout(timer);
      timer = 0;
    }
    if (stream) {
      stream.getTracks().forEach(function (t) {
        t.stop();
      });
      stream = null;
    }
    video.srcObject = null;
    view.hidden = true;
    stopBtn.hidden = true;
    startBtn.hidden = done; // nothing to restart once a code is on its way
  }

  function explain(err) {
    switch (err && err.name) {
      case "NotAllowedError":
      case "SecurityError":
        return box.dataset.denied;
      case "NotFoundError":
      case "OverconstrainedError":
        return box.dataset.missing;
      default:
        return box.dataset.failed;
    }
  }
})();
