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
const TIMEOUT_MS = 1000;

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
  var up=false;
  try{ await fetch('http://127.0.0.1:7999/_api/health',{mode:'no-cors',cache:'no-store'}); up=true; }catch(e){}
  if(up){
    msg.innerHTML='The daemon is running, but the <b>HTTPS redirect is down</b> &mdash; a VPN or firewall likely flushed it. Restore it with:';
    cmd.textContent='vibe doctor --fix';
  } else {
    msg.innerHTML='The vibe <b>daemon is not reachable</b>. If <code>vibe doctor --fix</code> does not help, the daemon may be down &mdash; start it with:';
    cmd.textContent='vibe daemon start';
  }
  setTimeout(function(){location.reload();}, 4000);
})();
</script>
</body></html>`;
