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
  border-radius:12px;padding:40px;max-width:420px;width:100%%;
  text-align:center;
  animation:fade-in .2s ease;
}
@keyframes fade-in{from{opacity:0;transform:translateY(4px)}to{opacity:1;transform:translateY(0)}}
h1{font-size:18px;font-weight:600;color:#fff;margin-bottom:4px}
.url{font-size:14px;color:var(--text-muted);margin-bottom:8px}
.status{display:flex;align-items:center;justify-content:center;gap:6px;font-size:13px;color:var(--text-muted);margin-bottom:24px}
.cmd{
  background:var(--bg);border:1px solid var(--border);border-radius:8px;
  padding:10px 14px;font-family:var(--font-mono);font-size:13px;color:var(--text-secondary);
  margin-bottom:24px;text-align:left;word-break:break-all;
}
.start-btn{
  background:#fff;color:#000;border:none;border-radius:8px;
  padding:10px 32px;font-size:14px;font-family:var(--font-sans);
  cursor:pointer;font-weight:500;transition:all .15s;
}
.start-btn:hover{background:#ededed}
.start-btn:disabled{background:var(--surface);color:var(--text-muted);cursor:default;border:1px solid var(--border)}
.page-spinner{
  display:none;margin:16px auto 0;width:20px;height:20px;
  border:1.5px solid #333;border-top-color:#888;
  border-radius:50%%;animation:spin .6s linear infinite;
}
.msg{color:var(--text-muted);font-size:13px;margin-top:10px;display:none}
</style>
</head>
<body>
<div class="card">
  <h1>%[3]s</h1>
  <div class="url">%[3]s.%[4]s</div>
  <div class="status"><span class="dot dot-red"></span>Stopped</div>
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
      btn.textContent='Start';
      spinner.style.display='none';
      msg.textContent='Failed to start';
      msg.style.color='#ff4d4f';
    }
  };
  xhr.onerror=function(){
    btn.disabled=false;
    btn.textContent='Start';
    spinner.style.display='none';
    msg.textContent='Could not reach daemon';
    msg.style.color='#ff4d4f';
  };
  xhr.send();
}

function pollUntilReady(attempts){
  if(attempts>60){
    document.getElementById('msg').textContent='Taking too long \u2014 check logs';
    document.getElementById('msg').style.color='#faad14';
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
        document.getElementById('msg').style.color='#52c41a';
        setTimeout(function(){window.location.href=window.location.href;},300);
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
