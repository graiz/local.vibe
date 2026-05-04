package daemon

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"path/filepath"
)

// serveStartPage renders a "not running" page for managed routes whose process
// has stopped. It offers a Start button that launches the process and polls
// until the port is accepting connections, then reloads.
//
// If the route has a stored Failure with a Recovery hint (or the log tail
// still reveals one after a daemon restart wiped the in-memory state), the
// recovery section is rendered visibly on page load so the user sees the
// "Kill PID X and retry" button without having to click Start first.
func (s *Server) serveStartPage(w http.ResponseWriter, _ *http.Request, route *Route) {
	tld := s.cfg.Daemon.TLD
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Route names are already validated to [a-z0-9-], but we escape on output
	// defensively so this template is safe against any future relaxation. Cmd
	// is free-form user input and MUST be escaped before HTML injection.
	safeName := html.EscapeString(route.Name)
	safeTLD := html.EscapeString(tld)
	safeCmd := html.EscapeString(route.Cmd)

	initRecovery := s.startPageRecoveryInitJS(route)

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
%s
<style>%s

/* Start page */
body{display:flex;align-items:center;justify-content:center}
.card{
  background:var(--surface);border:1px solid var(--border);
  border-radius:var(--radius-lg);padding:40px;max-width:420px;width:100%%;
  text-align:center;
  box-shadow:0 16px 48px rgba(0,0,0,.4);
  animation:fade-in .2s ease;
}
@keyframes fade-in{from{opacity:0;transform:translateY(4px)}to{opacity:1;transform:translateY(0)}}
h1{font-family:var(--font-display);font-size:1.1rem;font-weight:400;color:var(--amber);margin-bottom:4px;text-transform:uppercase;letter-spacing:.08em}
.url{font-size:.85rem;color:var(--text-muted);margin-bottom:8px}
.status{display:flex;align-items:center;justify-content:center;gap:6px;font-size:.8rem;color:var(--text-muted);margin-bottom:24px;text-transform:uppercase;letter-spacing:.06em}
.cmd{
  background:var(--bg);border:1px solid var(--border);border-radius:var(--radius);
  padding:10px 14px;font-family:var(--font-body);font-size:.8rem;color:var(--text-secondary);
  margin-bottom:24px;text-align:left;word-break:break-all;
}
.start-btn{
  background:var(--amber);color:var(--bg);border:none;border-radius:var(--radius);
  padding:10px 32px;font-size:.85rem;font-family:var(--font-body);font-weight:600;
  cursor:pointer;text-transform:uppercase;letter-spacing:.06em;transition:all .15s;
}
.start-btn:hover{background:#f0a048;box-shadow:0 0 12px var(--amber-glow)}
.start-btn:disabled{background:var(--surface);color:var(--text-muted);cursor:default;border:1px solid var(--border);box-shadow:none}
.page-spinner{
  display:none;margin:16px auto 0;width:20px;height:20px;
  border:1.5px solid #333;border-top-color:#888;
  border-radius:50%%;animation:spin .6s linear infinite;
}
.msg{color:var(--text-muted);font-size:.8rem;margin-top:10px;display:none}
.recovery{
  display:none;margin-top:16px;padding:14px;
  background:rgba(239,68,68,.06);border:1px solid rgba(239,68,68,.25);
  border-radius:var(--radius);text-align:left;
}
.recovery-msg{font-size:.82rem;color:var(--text-secondary);margin-bottom:10px;line-height:1.4}
.recovery-btn{
  background:var(--red,#ef4444);color:#fff;border:none;border-radius:var(--radius);
  padding:8px 16px;font-size:.78rem;font-family:var(--font-body);font-weight:600;
  cursor:pointer;text-transform:uppercase;letter-spacing:.06em;transition:all .15s;
}
.recovery-btn:hover{filter:brightness(1.1)}
.recovery-btn:disabled{opacity:.5;cursor:default}
.log-tail{
  margin-top:10px;padding:8px;background:var(--bg);border:1px solid var(--border);
  border-radius:var(--radius);font-family:var(--font-body);font-size:.72rem;
  color:var(--text-muted);white-space:pre-wrap;max-height:160px;overflow:auto;
}
</style>
</head>
<body>
<div class="card">
  <h1>%[3]s</h1>
  <div class="url">%[3]s.%[4]s</div>
  <div class="status"><span class="led led-red"></span>Stopped</div>
  <div class="cmd">%[5]s</div>
  <button class="start-btn" id="btn" onclick="handlePrimaryButton()">Cancel</button>
  <div class="page-spinner" id="spinner" style="display:block"></div>
  <div class="msg" id="msg" style="display:block;color:var(--text-muted)">Launching process…</div>
  <div class="recovery" id="recovery">
    <div class="recovery-msg" id="recovery-msg"></div>
    <button class="recovery-btn" id="recovery-btn" onclick="recoverAndRetry()"></button>
    <div class="log-tail" id="log-tail" style="display:none"></div>
  </div>
</div>
<script>
var lastRecovery=null;
var launchCanceled=false;
var activeXHR=null;
var launchMode='cancel'; // cancel | retry

function setLaunchMode(mode){
  launchMode=mode;
  var btn=document.getElementById('btn');
  if(mode==='cancel'){
    btn.disabled=false;
    btn.textContent='Cancel';
    return;
  }
  btn.disabled=false;
  btn.textContent='Retry';
}

function handlePrimaryButton(){
  if(launchMode==='cancel'){
    cancelLaunch();
    return;
  }
  startApp();
}

// fetchFailureAndRender pulls the most recent Failure off /ready and renders
// it on the page. Retries up to 3 times at 1s intervals to cover the race
// where the frontend poll timeout fires a beat before waitForReady writes
// its failure record.
function fetchFailureAndRender(tries){
  var msg=document.getElementById('msg');
  var spinner=document.getElementById('spinner');
  var fx=new XMLHttpRequest();
  fx.open('GET','/_api/routes/%[3]s/ready');
  fx.timeout=2000;
  fx.onload=function(){
    var d=null;try{d=JSON.parse(fx.responseText);}catch(e){}
    if(d&&d.failure){
      setLaunchMode('retry');spinner.style.display='none';
      msg.style.whiteSpace='pre-wrap';msg.style.textAlign='left';msg.style.fontSize='.75rem';
      msg.textContent=d.failure.message||'Server never became ready';
      msg.style.color='var(--red)';
      if(d.failure.recovery){showRecovery(d.failure.recovery, d.failure.log||'');}
      else if(d.failure.log){showFailureLog(d.failure.log);}
      return;
    }
    if(tries<3){setTimeout(function(){fetchFailureAndRender(tries+1);},1000);return;}
    setLaunchMode('retry');spinner.style.display='none';
    msg.textContent='Taking too long \u2014 check logs';
    msg.style.color='var(--yellow)';
  };
  fx.onerror=fx.ontimeout=function(){
    if(tries<3){setTimeout(function(){fetchFailureAndRender(tries+1);},1000);return;}
    setLaunchMode('retry');spinner.style.display='none';
    msg.textContent='Taking too long \u2014 check logs';
    msg.style.color='var(--yellow)';
  };
  fx.send();
}

function hideRecovery(){
  document.getElementById('recovery').style.display='none';
  lastRecovery=null;
}

function showRecovery(rec, logTail){
  lastRecovery=rec;
  var box=document.getElementById('recovery');
  var btn=document.getElementById('recovery-btn');
  btn.style.display='inline-flex';
  btn.disabled=false;
  document.getElementById('recovery-msg').textContent=rec.message||'Recoverable error';
  var label='Kill and Retry';
  if(rec.action==='kill_pid' && rec.pid) label='Kill PID '+rec.pid+' and Retry';
  else if(rec.action==='kill_port' && rec.port) label='Free Port '+rec.port+' and Retry';
  else if(rec.action==='restart') label=(rec.pid?'Restart Process (PID '+rec.pid+')':'Restart Process');
  btn.textContent=label;
  var logEl=document.getElementById('log-tail');
  if(logTail){logEl.textContent=logTail;logEl.style.display='block';}else{logEl.style.display='none';}
  box.style.display='block';
}

function showFailureLog(logTail){
  lastRecovery=null;
  var box=document.getElementById('recovery');
  var btn=document.getElementById('recovery-btn');
  var logEl=document.getElementById('log-tail');
  document.getElementById('recovery-msg').textContent='No automatic recovery available. See log tail:';
  btn.style.display='none';
  if(logTail){logEl.textContent=logTail;logEl.style.display='block';}else{logEl.style.display='none';}
  box.style.display='block';
}

function startApp(body){
  launchCanceled=false;
  hideRecovery();
  var spinner=document.getElementById('spinner');
  var msg=document.getElementById('msg');
  setLaunchMode('cancel');
  spinner.style.display='block';
  msg.style.display='block';
  msg.style.color='var(--text-muted)';
  msg.style.whiteSpace='normal';msg.style.textAlign='center';msg.style.fontSize='.8rem';
  msg.textContent='Launching process\u2026';

  var xhr=new XMLHttpRequest();
  activeXHR=xhr;
  xhr.open('POST','/_api/routes/%[3]s/start');
  xhr.setRequestHeader('Accept','application/json');
  if(body){xhr.setRequestHeader('Content-Type','application/json');}
  xhr.onload=function(){
    activeXHR=null;
    if(launchCanceled){return;}
    if(xhr.status===200){
      msg.textContent='Waiting for server\u2026';
      pollUntilReady(0);
    }else{
      setLaunchMode('retry');
      spinner.style.display='none';
      var errMsg='Failed to start';
      var d=null;
      try{d=JSON.parse(xhr.responseText);if(d&&d.error)errMsg=d.error;}catch(e){}
      msg.style.whiteSpace='pre-wrap';msg.style.textAlign='left';msg.style.fontSize='.75rem';
      msg.textContent=errMsg;
      msg.style.color='var(--red)';
      if(d&&d.recovery){showRecovery(d.recovery, d.log||'');}
      else if(d&&d.log){showFailureLog(d.log);}
    }
  };
  xhr.onerror=function(){
    activeXHR=null;
    if(launchCanceled){return;}
    setLaunchMode('retry');
    spinner.style.display='none';
    msg.textContent='Could not reach daemon';
    msg.style.color='var(--red)';
  };
  xhr.send(body?JSON.stringify(body):null);
}

function cancelLaunch(){
  launchCanceled=true;
  if(activeXHR){
    try{activeXHR.abort();}catch(e){}
    activeXHR=null;
  }
  var btn=document.getElementById('btn');
  var spinner=document.getElementById('spinner');
  var msg=document.getElementById('msg');
  btn.disabled=true;
  var stop=new XMLHttpRequest();
  stop.open('DELETE','/_api/routes/%[3]s/stop');
  stop.onload=function(){
    spinner.style.display='none';
    msg.style.display='block';
    msg.style.whiteSpace='normal';msg.style.textAlign='center';msg.style.fontSize='.8rem';
    msg.textContent='Start canceled';
    msg.style.color='var(--yellow)';
    setLaunchMode('retry');
  };
  stop.onerror=function(){
    spinner.style.display='none';
    msg.style.display='block';
    msg.textContent='Could not cancel cleanly — safe to retry';
    msg.style.color='var(--yellow)';
    setLaunchMode('retry');
  };
  stop.send();
}

function recoverAndRetry(){
  if(!lastRecovery)return;
  document.getElementById('recovery-btn').disabled=true;
  // "restart" means: stop the stuck child, then start fresh. Used when the
  // process is alive but never bound its port — safeKillPID refuses to
  // signal managed-owned PIDs, so a plain kill_pid won't work here.
  if(lastRecovery.action==='restart'){
    var stop=new XMLHttpRequest();
    stop.open('DELETE','/_api/routes/%[3]s/stop');
    stop.onload=function(){setTimeout(function(){startApp();},300);};
    stop.onerror=function(){startApp();};
    stop.send();
    return;
  }
  var body={};
  if(lastRecovery.action==='kill_pid'&&lastRecovery.pid)body.kill_pid=lastRecovery.pid;
  else if(lastRecovery.action==='kill_port'&&lastRecovery.port)body.kill_port=lastRecovery.port;
  else return;
  startApp(body);
}

// Poll ~35s — slightly longer than waitForReady's 30s deadline so the
// backend's Failure record is reliably in place when we ask for it.
function pollUntilReady(attempts){
  if(launchCanceled){return;}
  if(attempts>70){
    // Poll stopped making progress. Ask /ready once more: the backend's
    // waitForReady writes a Failure (log tail + recovery hint or a
    // "restart" fallback) when its own deadline expires, so we can show
    // the tail and a one-click recovery button instead of just "check logs".
    var msg=document.getElementById('msg');
    var btn=document.getElementById('btn');
    var spinner=document.getElementById('spinner');
    fetchFailureAndRender(0);
    return;
  }
  var xhr=new XMLHttpRequest();
  xhr.open('GET','/_api/routes/%[3]s/ready');
  xhr.timeout=2000;
  xhr.onload=function(){
    try{
      var d=JSON.parse(xhr.responseText);
      if(d.ready){
        document.getElementById('msg').textContent='Ready';
        document.getElementById('msg').style.color='var(--green)';
        setTimeout(function(){window.location.href=window.location.href;},300);
        return;
      }
      if(d.running===false){
        var msgEl=document.getElementById('msg');
        msgEl.style.whiteSpace='pre-wrap';msgEl.style.textAlign='left';msgEl.style.fontSize='.75rem';
        msgEl.textContent=(d.failure&&d.failure.message)?d.failure.message:'Process crashed \u2014 check logs at ~/.vibe/%[3]s.log';
        msgEl.style.color='var(--red)';
        setLaunchMode('retry');
        document.getElementById('spinner').style.display='none';
        if(d.failure&&d.failure.recovery){showRecovery(d.failure.recovery, d.failure.log||'');}
        else if(d.failure&&d.failure.log){showFailureLog(d.failure.log);}
        return;
      }
    }catch(e){}
    setTimeout(function(){pollUntilReady(attempts+1);},500);
  };
  xhr.onerror=function(){setTimeout(function(){pollUntilReady(attempts+1);},500);};
  xhr.ontimeout=function(){setTimeout(function(){pollUntilReady(attempts+1);},500);};
  xhr.send();
}
%[6]s
startApp();
</script>
</body>
</html>
`, themeHead(route.Name+"."+tld), themeCSS, safeName, safeTLD, safeCmd, initRecovery)
}

// startPageRecoveryInitJS returns JavaScript that calls showRecovery(...) on
// load when a known-bad condition already exists for the route (e.g., an
// orphan dev server on another port that the last crash identified).
// Returns an empty string when there's nothing actionable to surface.
func (s *Server) startPageRecoveryInitJS(route *Route) string {
	failure := route.LoadFailure()
	if failure == nil || failure.Recovery == nil {
		// Stored state may be gone (daemon restart). Scan the log as a
		// best-effort fallback so visibility isn't tied to in-memory state.
		logPath := filepath.Join(s.configDir(), route.Name+".log")
		tail := tailLogFile(logPath, 24)
		if tail == "" {
			return ""
		}
		rec := scanLogForRecovery(tail)
		if rec == nil {
			return ""
		}
		failure = &Failure{Message: "Previous start attempt failed", Log: tail, Recovery: rec}
	}
	recJSON, err := json.Marshal(failure.Recovery)
	if err != nil {
		return ""
	}
	logJSON, err := json.Marshal(failure.Log)
	if err != nil {
		return ""
	}
	// json.Marshal escapes <, >, & to \u003c etc., so the output is safe to
	// inline inside a <script> block without closing the tag prematurely.
	return fmt.Sprintf("showRecovery(%s, %s);", recJSON, logJSON)
}
