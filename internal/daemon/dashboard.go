package daemon

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

// serveDashboard renders the main dashboard HTML page at local.vibe.
func (s *Server) serveDashboard(w http.ResponseWriter, r *http.Request) {
	routes := s.table.List()
	tld := s.cfg.Daemon.TLD
	port := s.cfg.Daemon.Port

	host := r.Host
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}

	unknownName := ""
	if strings.HasSuffix(host, "."+tld) {
		name := strings.TrimSuffix(host, "."+tld)
		if name != "local" {
			if _, ok := s.table.Get(name); !ok {
				unknownName = name
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var b strings.Builder
	fmt.Fprintf(&b, `<!DOCTYPE html>
<html lang="en">
<head>
%s
<style>%s

/* Dashboard layout */
.page{max-width:1000px;margin:0 auto;padding:0 24px}

/* Nav bar */
.nav{
  display:flex;align-items:center;justify-content:space-between;
  height:64px;border-bottom:1px solid var(--border);
  margin-bottom:0;
}
.nav-left{display:flex;align-items:center;gap:12px}
.nav-logo{font-size:15px;font-weight:600;color:#fff;letter-spacing:-.01em}
.nav-logo span{color:var(--text-muted)}
.nav-status{display:flex;align-items:center;gap:6px;font-size:13px;color:var(--text-secondary)}
.nav-right{display:flex;align-items:center;gap:8px}

/* Toolbar */
.toolbar{
  display:flex;align-items:center;justify-content:space-between;
  padding:16px 0;border-bottom:1px solid var(--border);
}
.toolbar-left{display:flex;align-items:center;gap:16px}
.toolbar-title{font-size:14px;font-weight:500;color:var(--text)}
.toolbar-meta{font-size:13px;color:var(--text-muted)}

/* Warning banner */
.warn{
  display:flex;align-items:center;gap:8px;
  padding:12px 16px;border-bottom:1px solid var(--border);
  font-size:13px;color:var(--yellow-dot);background:transparent;
}

/* Route table */
.route-table{width:100%%;border-collapse:collapse}
.route-table th{
  font-size:12px;color:var(--text-muted);font-weight:400;
  text-align:left;padding:10px 12px 10px 0;
  border-bottom:1px solid var(--border);white-space:nowrap;
}
.route-table th:last-child{text-align:right}
.route-table td{
  padding:14px 12px 14px 0;
  border-bottom:1px solid var(--border);
  vertical-align:middle;font-size:13px;
}
.route-table tr:hover td{background:#080808}
.route-table td:last-child{text-align:right;padding-right:0}
.col-name{min-width:200px}
.col-type{width:120px;color:var(--text-muted);font-family:var(--font-mono);font-size:12px}
.col-port{width:80px;color:var(--text-muted);font-family:var(--font-mono)}
.col-age{width:80px;color:var(--text-muted)}
.col-status{width:80px;text-align:right}
.col-edit{width:40px;text-align:center;padding-right:0!important}
.name-cell{display:flex;align-items:center;gap:10px}
.name-primary{font-size:14px;font-weight:500;color:#fff;text-decoration:none;display:block;line-height:1.3}
.name-primary:hover{text-decoration:underline}
.name-secondary{font-size:12px;color:var(--text-muted);display:block;line-height:1.3;text-decoration:none}
.name-secondary:hover{color:var(--text-secondary)}
.idle-hint{font-size:11px;color:var(--text-muted);opacity:.6}
.btn-icon{background:transparent;border:none;color:var(--text-muted);cursor:pointer;padding:4px 6px;border-radius:4px;font-size:14px;transition:color .15s}
.btn-icon:hover{color:var(--text)}

/* Empty state */
.empty{text-align:center;padding:48px 0;color:var(--text-muted);font-size:14px}

/* Setup section */
.setup-section{padding:32px 0;border-top:1px solid var(--border);margin-top:32px}
.setup-title{font-size:14px;font-weight:500;color:var(--text);margin-bottom:12px}
.setup-desc{font-size:13px;color:var(--text-muted);line-height:1.6;margin-bottom:16px}
.copy-wrap{}
.copy-area{
  width:100%%;background:#111;
  border:1px solid #222;border-radius:8px;
  padding:16px 18px;font-family:var(--font-mono);font-size:13px;
  color:#ccc;outline:none;resize:none;line-height:1.8;height:8.5rem;
  display:block;
}
.copy-area:focus{border-color:#444}
.copy-bar{display:flex;justify-content:flex-end;margin-top:10px}
.copy-btn{
  background:var(--bg);border:1px solid var(--border);
  border-radius:6px;padding:6px 14px;color:var(--text-secondary);
  cursor:pointer;display:inline-flex;align-items:center;gap:6px;
  font-family:var(--font-sans);font-size:13px;font-weight:500;
  transition:all .15s;
}
.copy-btn:hover{border-color:#444;color:var(--text)}

/* Footer */
.footer{
  padding:16px 0;
  border-top:1px solid var(--border);
  color:var(--text-muted);font-size:12px;
  display:flex;gap:16px;align-items:center;
  margin-top:32px;
}
.footer a{color:var(--text-muted);transition:color .15s}
.footer a:hover{color:var(--text-secondary)}

@media(max-width:700px){
  .col-type,.col-port,.col-age{display:none}
  .route-table th.col-type,.route-table th.col-port,.route-table th.col-age{display:none}
}
</style>
</head>
<body>
<div class="page">
`, themeHead("local.vibe"), themeCSS)

	// Nav bar
	uptime := time.Since(s.startedAt).Round(time.Second)
	fmt.Fprintf(&b, `<nav class="nav">
  <div class="nav-left">
    <span class="nav-logo">local<span>.vibe</span></span>
    <span class="nav-status"><span class="dot dot-green"></span>Running &middot; %s</span>
  </div>
  <div class="nav-right">
    <button class="btn" onclick="openAddModal()">Add Route</button>
  </div>
</nav>
`, fmtDuration(uptime))

	// Warning banner
	if unknownName != "" {
		fmt.Fprintf(&b, `<div class="warn"><span class="dot dot-yellow"></span>No route for <strong style="color:var(--text);margin:0 4px">%s.%s</strong> — register it below.</div>`,
			html.EscapeString(unknownName), tld)
	}

	// Toolbar
	fmt.Fprintf(&b, `<div class="toolbar">
  <div class="toolbar-left">
    <span class="toolbar-title">Routes</span>
    <span class="toolbar-meta">%d total</span>
  </div>
</div>
`, len(routes)+1) // +1 for self

	// Table
	b.WriteString(`<table class="route-table">
<thead><tr>
  <th>Name</th>
  <th class="col-type">Type</th>
  <th class="col-port">Port</th>
  <th class="col-age">Age</th>
  <th class="col-status"></th>
  <th class="col-edit"></th>
</tr></thead>
<tbody>`)

	// Self row
	fmt.Fprintf(&b, `<tr>
  <td class="col-name"><div class="name-cell"><span class="dot dot-green"></span><div><a href="http://local.%s/" class="name-primary">local</a><a href="http://local.%s/" class="name-secondary">local.%s</a></div></div></td>
  <td class="col-type">daemon</td>
  <td class="col-port">%d</td>
  <td class="col-age">%s</td>
  <td class="col-status"></td>
  <td class="col-edit"></td>
</tr>`, tld, tld, tld, port, fmtDuration(uptime))

	if len(routes) == 0 {
		b.WriteString(`</tbody></table>`)
		b.WriteString(`<div class="empty">No routes registered yet.</div>`)
	} else {
		for _, r := range routes {
			safeName := html.EscapeString(r.Name)
			vibeURL := fmt.Sprintf("http://%s.%s", safeName, tld)

			// Status dot: green=ready, yellow=starting, red=stopped
			dotClass := "dot-gray"
			switch r.Type {
			case RouteManaged:
				if s.isPortReady(r.Port) {
					dotClass = "dot-green"
				} else if r.Running.Load() {
					dotClass = "dot-yellow" // process alive, port not yet listening
				} else {
					dotClass = "dot-red"
				}
			case RouteSticky:
				dotClass = "dot-green"
			case RoutePIDTracked:
				dotClass = "dot-green"
			case RouteBookmark:
				dotClass = "dot-yellow"
			}

			// Display URL
			urlDisplay := fmt.Sprintf("%s.%s", safeName, tld)
			if r.Type == RouteBookmark {
				urlDisplay = html.EscapeString(r.ExternalURL)
			}

			// Type label
			typeLabel := string(r.Type)
			idleStr := ""
			if r.Type == RouteManaged && r.IdleTimeout > 0 {
				if r.IdleTimeout >= 60 {
					idleStr = fmt.Sprintf(` <span class="route-col-idle">(%dh idle)</span>`, r.IdleTimeout/60)
				} else {
					idleStr = fmt.Sprintf(` <span class="route-col-idle">(%dm idle)</span>`, r.IdleTimeout)
				}
			}

			// Port
			portStr := fmt.Sprintf("%d", r.Port)
			if r.Type == RouteBookmark {
				portStr = "—"
			}

			// Age
			since := time.Since(r.RegisteredAt).Round(time.Second)

			// Status action (Start/Stop button)
			statusBtn := ""
			if r.Type == RouteManaged && s.isPortReady(r.Port) {
				statusBtn = fmt.Sprintf(`<button class="btn" onclick="routeAction(this,'%s','stop')">Stop</button>`, safeName)
			} else if r.Type == RouteManaged {
				statusBtn = fmt.Sprintf(`<button class="btn btn-primary" onclick="routeAction(this,'%s','start')">Start</button>`, safeName)
			}

			// Edit/settings action (always in its own column)
			editBtn := ""
			if r.Type == RouteManaged {
				editBtn = fmt.Sprintf(`<button class="btn-icon" onclick="openManagedModal('%s',%d)" title="Settings">&#9998;</button>`, safeName, r.IdleTimeout)
			} else if r.Type == RouteSticky || r.Type == RouteBookmark {
				safeExtURL := html.EscapeString(r.ExternalURL)
				editType := "local"
				if r.Type == RouteBookmark {
					editType = "bookmark"
				}
				editBtn = fmt.Sprintf(`<button class="btn-icon" onclick="openEditModal('%s',%d,'%s','%s')" title="Edit">&#9998;</button>`,
					safeName, r.Port, safeExtURL, editType)
			}

			fmt.Fprintf(&b, `<tr>
  <td class="col-name"><div class="name-cell"><span class="dot %s"></span><div><a href="%s" target="_blank" class="name-primary">%s</a><a href="%s" target="_blank" class="name-secondary">%s</a></div></div></td>
  <td class="col-type">%s %s</td>
  <td class="col-port">%s</td>
  <td class="col-age">%s</td>
  <td class="col-status">%s</td>
  <td class="col-edit">%s</td>
</tr>`, dotClass, vibeURL, safeName, vibeURL, urlDisplay,
				typeLabel, idleStr, portStr, fmtDuration(since), statusBtn, editBtn)
		}
		b.WriteString(`</tbody></table>`)
	}

	// Setup section
	fmt.Fprintf(&b, `<div class="setup-section">
  <div class="setup-title">Get Started</div>
  <p class="setup-desc">
    Paste this into Claude Code, Cursor, or any agentic IDE to configure a project with local.vibe.
  </p>
  <div class="copy-wrap">
    <textarea id="setup-text" readonly class="copy-area">This machine is running local.vibe, a local DNS tool that gives dev servers friendly .%[1]s names instead of hard-to-remember port numbers.

Read the full setup instructions:

  curl http://localhost:7999/setup.md

Or open in a browser: http://local.%[1]s/setup.md</textarea>
    <div class="copy-bar">
      <button onclick="copyText()" class="copy-btn" id="copy-btn">
        <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor"><path d="M0 6.75C0 5.784.784 5 1.75 5h1.5a.75.75 0 010 1.5h-1.5a.25.25 0 00-.25.25v7.5c0 .138.112.25.25.25h7.5a.25.25 0 00.25-.25v-1.5a.75.75 0 011.5 0v1.5A1.75 1.75 0 019.25 16h-7.5A1.75 1.75 0 010 14.25v-7.5z"/><path d="M5 1.75C5 .784 5.784 0 6.75 0h7.5C15.216 0 16 .784 16 1.75v7.5A1.75 1.75 0 0114.25 11h-7.5A1.75 1.75 0 015 9.25v-7.5zm1.75-.25a.25.25 0 00-.25.25v7.5c0 .138.112.25.25.25h7.5a.25.25 0 00.25-.25v-7.5a.25.25 0 00-.25-.25h-7.5z"/></svg>
        Copy
      </button>
    </div>
  </div>
</div>
<!-- Add/Edit Modal -->
<div class="modal-overlay" id="modal-overlay" onclick="if(event.target===this)closeModal()">
<div class="modal">
  <h3 id="modal-title">Add Route</h3>
  <div class="type-toggle" id="type-toggle">
    <button id="toggle-local" class="active" onclick="setRouteType('local')">Local Port</button>
    <button id="toggle-bookmark" onclick="setRouteType('bookmark')">External URL</button>
  </div>
  <label for="route-name">Name</label>
  <input id="route-name" type="text" placeholder="myapp" autocomplete="off">
  <div id="port-field">
    <label for="route-port">Port</label>
    <input id="route-port" type="number" placeholder="3000">
  </div>
  <div id="url-field" style="display:none">
    <label for="route-url">URL</label>
    <input id="route-url" type="url" placeholder="https://example.com:8080/">
  </div>
  <div class="modal-actions">
    <button class="btn btn-danger" id="modal-delete" style="display:none;margin-right:auto" onclick="deleteRoute()">Delete</button>
    <button class="btn" onclick="closeModal()">Cancel</button>
    <button class="btn btn-primary" id="modal-save" onclick="saveRoute()">Add</button>
  </div>
</div>
</div>
<!-- Managed Route Settings Modal -->
<div class="modal-overlay" id="managed-overlay" onclick="if(event.target===this)closeManagedModal()">
<div class="modal">
  <h3>Route Settings</h3>
  <label for="idle-timeout">Auto-stop after idle (minutes)</label>
  <input id="idle-timeout" type="number" min="0" placeholder="0 = never">
  <p class="hint">Stop the process automatically when no traffic is received. Set to 0 to disable.</p>
  <div class="modal-actions">
    <button class="btn" onclick="closeManagedModal()">Cancel</button>
    <button class="btn btn-primary" onclick="saveManagedSettings()">Save</button>
  </div>
</div>
</div>
<script>
var modalMode='add';
var editingName='';
function setRouteType(t){
  document.getElementById('toggle-local').className=t==='local'?'active':'';
  document.getElementById('toggle-bookmark').className=t==='bookmark'?'active':'';
  document.getElementById('port-field').style.display=t==='local'?'':'none';
  document.getElementById('url-field').style.display=t==='bookmark'?'':'none';
}
function openAddModal(){
  modalMode='add';editingName='';
  document.getElementById('modal-title').textContent='Add Route';
  document.getElementById('modal-save').textContent='Add';
  document.getElementById('modal-delete').style.display='none';
  document.getElementById('type-toggle').style.display='';
  document.getElementById('route-name').value='';
  document.getElementById('route-name').disabled=false;
  document.getElementById('route-port').value='';
  document.getElementById('route-url').value='';
  setRouteType('local');
  document.getElementById('modal-overlay').classList.add('active');
}
function openEditModal(name,port,url,type){
  modalMode='edit';editingName=name;
  document.getElementById('modal-title').textContent='Edit Route';
  document.getElementById('modal-save').textContent='Save';
  document.getElementById('modal-delete').style.display='';
  document.getElementById('type-toggle').style.display='';
  document.getElementById('route-name').value=name;
  document.getElementById('route-name').disabled=false;
  if(type==='bookmark'){
    setRouteType('bookmark');
    document.getElementById('route-url').value=url;
    document.getElementById('route-port').value='';
  }else{
    setRouteType('local');
    document.getElementById('route-port').value=port||'';
    document.getElementById('route-url').value='';
  }
  document.getElementById('modal-overlay').classList.add('active');
}
function closeModal(){document.getElementById('modal-overlay').classList.remove('active')}
function saveRoute(){
  var name=document.getElementById('route-name').value.trim();
  if(!name)return;
  var isBookmark=document.getElementById('toggle-bookmark').className==='active';
  var body={name:name};
  if(isBookmark){
    var u=document.getElementById('route-url').value.trim();
    if(!u)return;
    body.url=u;
  }else{
    var p=parseInt(document.getElementById('route-port').value);
    if(!p)return;
    body.port=p;
  }
  if(modalMode==='edit'){
    fetch('/_api/routes/'+encodeURIComponent(editingName),{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}).then(function(){location.reload()});
  }else{
    fetch('/_api/routes',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}).then(function(){location.reload()});
  }
}
function deleteRoute(){
  if(!editingName)return;
  fetch('/_api/routes/'+encodeURIComponent(editingName),{method:'DELETE',headers:{'Accept':'application/json'}}).then(function(){location.reload()});
}
function copyText(){
  var ta=document.getElementById('setup-text');
  if(navigator.clipboard&&window.isSecureContext){
    navigator.clipboard.writeText(ta.value).then(showCheck);
  }else{
    ta.select();document.execCommand('copy');window.getSelection().removeAllRanges();showCheck();
  }
}
var managedEditName='';
function openManagedModal(name,idle){
  managedEditName=name;
  document.getElementById('idle-timeout').value=idle||'';
  document.getElementById('managed-overlay').classList.add('active');
}
function closeManagedModal(){document.getElementById('managed-overlay').classList.remove('active')}
function saveManagedSettings(){
  var idle=parseInt(document.getElementById('idle-timeout').value)||0;
  fetch('/_api/routes/'+encodeURIComponent(managedEditName),{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({idle_timeout:idle})}).then(function(){location.reload()});
}
function routeAction(btn,name,action){
  var origText=btn.textContent;
  btn.disabled=true;
  btn.innerHTML='<span class="spinner"></span>';
  fetch('/_api/routes/'+encodeURIComponent(name)+'/'+action,{method:'POST',headers:{'Accept':'application/json'}})
    .then(function(r){return r.json()})
    .then(function(){
      if(action==='start'){pollReady(name,0)}
      else{pollStopped(name,0)}
    })
    .catch(function(){btn.disabled=false;btn.textContent=origText});
}
function pollStopped(name,n){
  if(n>20){location.reload();return}
  fetch('/_api/routes/'+encodeURIComponent(name)+'/ready')
    .then(function(r){return r.json()})
    .then(function(d){
      if(!d.ready){location.reload()}
      else{setTimeout(function(){pollStopped(name,n+1)},300)}
    })
    .catch(function(){location.reload()});
}
function pollReady(name,n){
  if(n>60){location.reload();return}
  fetch('/_api/routes/'+encodeURIComponent(name)+'/ready')
    .then(function(r){return r.json()})
    .then(function(d){
      if(d.ready){location.reload()}
      else{setTimeout(function(){pollReady(name,n+1)},500)}
    })
    .catch(function(){setTimeout(function(){pollReady(name,n+1)},500)});
}
function showCheck(){
  var btn=document.getElementById('copy-btn');
  var orig=btn.innerHTML;
  btn.innerHTML='<svg width="14" height="14" viewBox="0 0 16 16" fill="#52c41a"><path d="M13.78 4.22a.75.75 0 010 1.06l-7.25 7.25a.75.75 0 01-1.06 0L2.22 9.28a.75.75 0 011.06-1.06L6 10.94l6.72-6.72a.75.75 0 011.06 0z"/></svg> Copied';
  setTimeout(function(){btn.innerHTML=orig},1500);
}
document.addEventListener('visibilitychange',function(){
  if(!document.hidden){location.reload()}
});
</script>
`, tld)

	// Footer
	fmt.Fprintf(&b, `<div class="footer">
  <span>daemon :%d</span>
  <a href="/_api/routes">API</a>
  <a href="/_api/health">Health</a>
</div>
`, port)

	b.WriteString(`</div></body></html>`)
	fmt.Fprint(w, b.String())
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func fmtDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
}
