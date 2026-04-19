package daemon

import (
	"fmt"
	"html"
	"net/http"
)

// serveRepairPage renders a "reconnecting..." page for a route whose
// registered port is no longer answering. The page polls
// /_api/routes/{name}/repair in the background; on a successful hit the
// daemon silently rewrites the route's port and the page reloads into a
// working proxy. On failure it offers a Restart button for managed
// routes (when the child process is gone) or a link back to the
// dashboard.
func (s *Server) serveRepairPage(w http.ResponseWriter, _ *http.Request, route *Route) {
	tld := s.cfg.Daemon.TLD
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	safeName := html.EscapeString(route.Name)
	safeTLD := html.EscapeString(tld)
	safePort := route.Port

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
%s
<style>%s

/* Repair page */
body{display:flex;align-items:center;justify-content:center}
.card{
  background:var(--surface);border:1px solid var(--border);
  border-radius:var(--radius-lg);padding:40px;max-width:440px;width:100%%;
  text-align:center;
  box-shadow:0 16px 48px rgba(0,0,0,.4);
  animation:fade-in .2s ease;
}
@keyframes fade-in{from{opacity:0;transform:translateY(4px)}to{opacity:1;transform:translateY(0)}}
h1{font-family:var(--font-display);font-size:1.1rem;font-weight:400;color:var(--yellow);margin-bottom:4px;text-transform:uppercase;letter-spacing:.08em}
.url{font-size:.85rem;color:var(--text-muted);margin-bottom:8px}
.status{display:flex;align-items:center;justify-content:center;gap:6px;font-size:.8rem;color:var(--text-muted);margin-bottom:20px;text-transform:uppercase;letter-spacing:.06em}
.status .led{display:inline-block;width:8px;height:8px;border-radius:50%%;background:var(--yellow);box-shadow:0 0 8px var(--yellow-glow);animation:pulse 1.2s ease-in-out infinite}
@keyframes pulse{0%%,100%%{opacity:.4}50%%{opacity:1}}
.hint{
  background:var(--bg);border:1px solid var(--border);border-radius:var(--radius);
  padding:10px 14px;font-family:var(--font-body);font-size:.8rem;color:var(--text-secondary);
  margin-bottom:20px;text-align:left;line-height:1.5;
}
.hint .label{color:var(--text-muted);text-transform:uppercase;letter-spacing:.06em;font-size:.72rem;margin-bottom:4px}
.hint code{color:var(--amber);font-family:var(--font-body)}
.page-spinner{
  display:block;margin:0 auto 14px;width:22px;height:22px;
  border:1.5px solid #333;border-top-color:var(--yellow);
  border-radius:50%%;animation:spin 1s linear infinite;
}
.msg{color:var(--text-muted);font-size:.8rem;margin-top:4px}
.actions{display:none;flex-direction:column;gap:10px;margin-top:18px}
.btn{
  background:var(--amber);color:var(--bg);border:none;border-radius:var(--radius);
  padding:10px 24px;font-size:.82rem;font-family:var(--font-body);font-weight:600;
  cursor:pointer;text-transform:uppercase;letter-spacing:.06em;transition:all .15s;
}
.btn:hover{background:#f0a048;box-shadow:0 0 12px var(--amber-glow)}
.btn:disabled{background:var(--surface);color:var(--text-muted);cursor:default;border:1px solid var(--border);box-shadow:none}
.btn.secondary{background:transparent;border:1px solid var(--border);color:var(--text-secondary)}
.btn.secondary:hover{border-color:var(--text-secondary);color:var(--text);box-shadow:none;background:var(--elevated)}
.error{color:var(--red);font-size:.8rem;margin-top:10px;white-space:pre-wrap;text-align:left}
</style>
</head>
<body>
<div class="card">
  <h1>%[3]s</h1>
  <div class="url">%[3]s.%[4]s</div>
  <div class="status"><span class="led"></span>Reconnecting</div>
  <div class="hint">
    <div class="label">Port %[5]d not responding</div>
    Looking for the app in logs and process listings&hellip;
  </div>
  <div class="page-spinner" id="spinner"></div>
  <div class="msg" id="msg">Probing&hellip;</div>
  <div class="actions" id="actions"></div>
  <div class="error" id="error" style="display:none"></div>
</div>
<script>
var attempts=0;
var maxAttempts=30;

function showActions(html){
  var box=document.getElementById('actions');
  box.innerHTML=html;
  box.style.display='flex';
}

function giveUp(reason, restartable){
  document.getElementById('spinner').style.display='none';
  var msg=document.getElementById('msg');
  msg.textContent='Could not recover automatically';
  msg.style.color='var(--red)';
  var err=document.getElementById('error');
  err.textContent=reason||'No listening port found nearby.';
  err.style.display='block';
  var buttons='';
  if(restartable){
    buttons+='<button class="btn" onclick="restartApp()">Restart app</button>';
  }
  buttons+='<button class="btn secondary" onclick="window.location.reload()">Retry</button>';
  buttons+='<a class="btn secondary" href="/" style="text-decoration:none;display:inline-block">Dashboard</a>';
  showActions(buttons);
}

function restartApp(){
  var btns=document.querySelectorAll('#actions .btn');
  btns.forEach(function(b){b.disabled=true;});
  document.getElementById('spinner').style.display='block';
  document.getElementById('msg').style.color='';
  document.getElementById('msg').textContent='Restarting\u2026';
  document.getElementById('error').style.display='none';
  document.getElementById('actions').style.display='none';
  var xhr=new XMLHttpRequest();
  xhr.open('POST','/_api/routes/%[3]s/start');
  xhr.setRequestHeader('Accept','application/json');
  xhr.onload=function(){
    if(xhr.status===200){
      // Fall back into the poll loop — the repair endpoint will see the new listener.
      attempts=0;
      setTimeout(pollRepair,500);
    }else{
      var err='Failed to start';
      try{var d=JSON.parse(xhr.responseText);if(d&&d.error)err=d.error;}catch(e){}
      giveUp(err, true);
    }
  };
  xhr.onerror=function(){giveUp('Could not reach daemon', true);};
  xhr.send();
}

function pollRepair(){
  if(attempts>=maxAttempts){
    giveUp('Still no listener after '+Math.round(maxAttempts*0.5)+'s. The app may have crashed.', false);
    return;
  }
  attempts++;
  var xhr=new XMLHttpRequest();
  xhr.open('GET','/_api/routes/%[3]s/repair');
  xhr.timeout=2500;
  xhr.onload=function(){
    var d=null;
    try{d=JSON.parse(xhr.responseText);}catch(e){}
    if(d&&d.ok){
      document.getElementById('msg').textContent='Found it \u2014 reloading';
      document.getElementById('msg').style.color='var(--green)';
      setTimeout(function(){window.location.href=window.location.href;},250);
      return;
    }
    if(d&&d.restartable){
      giveUp(d.reason||'Process is not running.', true);
      return;
    }
    // Keep trying for a bit; the app may still be starting up.
    setTimeout(pollRepair,500);
  };
  xhr.onerror=function(){setTimeout(pollRepair,500);};
  xhr.ontimeout=function(){setTimeout(pollRepair,500);};
  xhr.send();
}

pollRepair();
</script>
</body>
</html>
`, themeHead(route.Name+"."+tld), themeCSS, safeName, safeTLD, safePort)
}
