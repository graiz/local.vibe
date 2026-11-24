// pages.js — shared browser helpers for every daemon-served page (dashboard,
// start page, reconnecting page). Inlined via {{.JS}} like theme.css, so the
// no-external-assets rule holds. Keep it dependency-free and ES5-compatible.

// vibeAPI issues one request to the daemon API. cb(status, data, xhr) runs
// once on completion; status is 0 on a network error or timeout, data is the
// parsed JSON body or null. Returns the XHR so callers can abort it.
function vibeAPI(method, path, opts, cb) {
  opts = opts || {};
  var xhr = new XMLHttpRequest();
  xhr.open(method, path);
  xhr.setRequestHeader('Accept', 'application/json');
  if (opts.timeout) xhr.timeout = opts.timeout;
  var body = null;
  if (opts.body !== undefined && opts.body !== null) {
    xhr.setRequestHeader('Content-Type', 'application/json');
    body = JSON.stringify(opts.body);
  }
  var done = false;
  function finish(status) {
    if (done) return;
    done = true;
    var data = null;
    try { data = JSON.parse(xhr.responseText); } catch (e) {}
    cb(status, data, xhr);
  }
  xhr.onload = function () { finish(xhr.status); };
  xhr.onerror = function () { finish(0); };
  xhr.ontimeout = function () { finish(0); };
  xhr.send(body);
  return xhr;
}

// vibePoll calls step(next, giveUp) every intervalMs until step stops calling
// next() — up to maxTries times, after which onGiveUp runs. step decides what
// "done" means; this only owns the schedule.
function vibePoll(step, intervalMs, maxTries, onGiveUp) {
  var tries = 0;
  function tick() {
    if (tries >= maxTries) { if (onGiveUp) onGiveUp(); return; }
    tries++;
    step(function () { setTimeout(tick, intervalMs); });
  }
  tick();
}

// reloadSoon reloads the page after ms (default: at once).
function reloadSoon(ms) {
  setTimeout(function () { window.location.reload(); }, ms || 0);
}

// showToast surfaces a transient message bottom-of-page.
function showToast(msg) {
  var el = document.createElement('div');
  el.className = 'toast';
  el.textContent = msg;
  document.body.appendChild(el);
  setTimeout(function () { el.classList.add('show'); }, 10);
  setTimeout(function () {
    el.classList.remove('show');
    setTimeout(function () { el.remove(); }, 300);
  }, 4000);
}
