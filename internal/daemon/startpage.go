package daemon

import (
	"fmt"
	"net/http"
)

// serveStartPage renders a "not running" page for managed routes whose process
// has stopped. It offers a Start button that launches the process and polls
// until the port is accepting connections, then reloads.
func (s *Server) serveStartPage(w http.ResponseWriter, _ *http.Request, route *Route) {
	tld := s.cfg.Daemon.TLD
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

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
</style>
</head>
<body>
<div class="card">
  <h1>%[3]s</h1>
  <div class="url">%[3]s.%[4]s</div>
  <div class="status"><span class="led led-red"></span>Stopped</div>
  <div class="cmd">%[5]s</div>
  <button class="start-btn" id="btn" onclick="startApp()">Start</button>
  <div class="page-spinner" id="spinner"></div>
  <div class="msg" id="msg"></div>
</div>
<script>
function startApp(){
  var btn=document.getElementById('btn');
  var spinner=document.getElementById('spinner');
  var msg=document.getElementById('msg');
  btn.disabled=true;
  btn.textContent='Starting\u2026';
  spinner.style.display='block';
  msg.style.display='block';
  msg.textContent='Launching process\u2026';

  var xhr=new XMLHttpRequest();
  xhr.open('POST','/_api/routes/%[3]s/start');
  xhr.setRequestHeader('Accept','application/json');
  xhr.onload=function(){
    if(xhr.status===200){
      msg.textContent='Waiting for server\u2026';
      pollUntilReady(0);
    }else{
      btn.disabled=false;
      btn.textContent='Retry';
      spinner.style.display='none';
      var errMsg='Failed to start';
      try{var d=JSON.parse(xhr.responseText);if(d.error)errMsg=d.error;}catch(e){}
      msg.style.whiteSpace='pre-wrap';msg.style.textAlign='left';msg.style.fontSize='.75rem';
      msg.textContent=errMsg;
      msg.style.color='var(--red)';
    }
  };
  xhr.onerror=function(){
    btn.disabled=false;
    btn.textContent='Start';
    spinner.style.display='none';
    msg.textContent='Could not reach daemon';
    msg.style.color='var(--red)';
  };
  xhr.send();
}

function pollUntilReady(attempts){
  if(attempts>60){
    document.getElementById('msg').textContent='Taking too long \u2014 check logs';
    document.getElementById('msg').style.color='var(--yellow)';
    document.getElementById('btn').disabled=false;
    document.getElementById('btn').textContent='Retry';
    document.getElementById('spinner').style.display='none';
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
        document.getElementById('msg').textContent='Process crashed \u2014 check logs at ~/.vibe/%[3]s.log';
        document.getElementById('msg').style.color='var(--red)';
        document.getElementById('btn').disabled=false;
        document.getElementById('btn').textContent='Retry';
        document.getElementById('spinner').style.display='none';
        return;
      }
    }catch(e){}
    setTimeout(function(){pollUntilReady(attempts+1);},500);
  };
  xhr.onerror=function(){setTimeout(function(){pollUntilReady(attempts+1);},500);};
  xhr.ontimeout=function(){setTimeout(function(){pollUntilReady(attempts+1);},500);};
  xhr.send();
}
</script>
</body>
</html>
`, themeHead(route.Name+"."+tld), themeCSS, route.Name, tld, route.Cmd)
}
