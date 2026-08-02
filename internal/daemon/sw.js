// vibe service worker — offline fallback for local.__TLD__.
//
// When the privileged-port redirect (pf on macOS / portproxy on Windows) is
// flushed by a VPN or firewall, or the daemon is down, the browser can't reach
// the daemon at all and shows a bare "can't connect". This worker is stored by
// the browser, so it keeps running and serves a helpful page instead. It races
// the real request against a short timeout so a *hung* backend (not just a
// refused one) also falls back.
//
// Scope is local.__TLD__ only. Only top-level navigations are intercepted;
// everything else passes straight through.
//
// The timeout only exists to catch a *hung* backend (a socket that accepts but
// never responds). A refused/failed connection rejects fetch() immediately and
// falls back without waiting. It must stay comfortably above a healthy but slow
// dashboard render (the synchronous redirect probe plus many routes can take
// well over a second under load) — a too-tight timeout replaces a dashboard
// that was about to paint with the error page, then flaps on every reload.
const TIMEOUT_MS = 4000;

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('fetch', (event) => {
  if (event.request.mode !== 'navigate') return;
  event.respondWith(navOrFallback(event.request));
});

async function navOrFallback(req) {
  try {
    return await Promise.race([
      fetch(req),
      new Promise((_, reject) => setTimeout(() => reject(new Error('timeout')), TIMEOUT_MS)),
    ]);
  } catch (_) {
    return new Response(FALLBACK_HTML, {
      status: 503,
      headers: { 'Content-Type': 'text/html; charset=utf-8' },
    });
  }
}

const FALLBACK_HTML = `<!doctype html>
<html lang=en><head><meta charset=utf-8>
<meta name=viewport content="width=device-width,initial-scale=1">
<title>local.__TLD__ unreachable</title>
<style>
  body{margin:0;font:15px/1.6 -apple-system,system-ui,sans-serif;background:#111113;color:#e8e4de;display:grid;place-items:center;min-height:100vh}
  .card{max-width:560px;padding:32px;text-align:center}
  h1{font-size:18px;margin:0 0 14px;font-weight:600}
  .led{display:inline-block;width:9px;height:9px;border-radius:50%;background:#e54545;box-shadow:0 0 8px #e54545;margin-right:9px;vertical-align:middle}
  p{color:#b8b2a8;margin:10px 0}
  code{color:#e8943a}
  pre{background:#19191d;border:1px solid #2e2e38;border-radius:8px;padding:12px 18px;color:#e8943a;display:inline-block;margin:16px 0;font-size:14px}
  .dim{color:#605c54;font-size:13px}
</style></head>
<body><div class=card>
  <h1><span class=led></span>can't reach local.__TLD__</h1>
  <p id=msg>Checking what's wrong&hellip;</p>
  <pre id=cmd>vibe doctor --fix</pre>
  <p class=dim id=retry>Retrying automatically&hellip;</p>
</div>
<script>
(async function(){
  var msg=document.getElementById('msg'), cmd=document.getElementById('cmd');
  // The daemon's plain-HTTP port (7999) is bound directly, independent of the
  // redirect. If it answers, the daemon is up and only the redirect is broken;
  // if it does not, the daemon itself is likely down. Loopback fetch from a
  // secure context is allowed (potentially-trustworthy origin); a blocked probe
  // just falls through to the generic message.
  // Arm the retry FIRST. The probe below can hang indefinitely — a VPN
  // kill-switch that accepts the connection and forwards nothing is exactly
  // this page's reason to exist — and a retry armed only after the probe
  // settles would never fire, freezing the page on "Checking what's wrong".
  setTimeout(function(){location.reload();}, 6000);
  // The daemon's plain-HTTP port is bound directly, independent of the
  // redirect. Race the probe against a timeout so a hung connection is
  // reported as "unknown" rather than stalling.
  var up=null;
  try{
    var ctl=('AbortController' in self)?new AbortController():null;
    var timer=setTimeout(function(){ if(ctl) ctl.abort(); }, 2000);
    await Promise.race([
      fetch('http://127.0.0.1:__PORT__/_api/health',
            {mode:'no-cors',cache:'no-store',signal:ctl?ctl.signal:undefined}).then(function(){up=true;}),
      new Promise(function(r){ setTimeout(r, 2000); })
    ]);
    clearTimeout(timer);
  }catch(e){ up=false; }
  if(up===true){
    msg.innerHTML='The daemon is running, but the <b>HTTPS redirect is down</b> &mdash; a VPN or firewall likely flushed it. Restore it with:';
    cmd.textContent='vibe doctor --fix';
  } else {
    // up===false (probe refused) or up===null (probe timed out, or the browser
    // blocked the mixed-content loopback fetch — Safari does). Both mean the
    // probe is inconclusive about WHICH layer failed, so lead with the repair
    // that fixes the common case and mention the daemon as the fallback,
    // rather than asserting the daemon is down.
    msg.innerHTML='Could not reach vibe. The redirect is the usual cause &mdash; try this first. '+
      'If it does not help, the daemon may be down: run <code>vibe daemon start</code>.';
    cmd.textContent='vibe doctor --fix';
  }
})();
</script>
</body></html>`;
