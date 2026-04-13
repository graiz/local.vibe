package daemon

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

// serveDashboard renders the main dashboard HTML page at local.vibe.
func (s *Server) vibeScheme() string {
	if s.cfg.Daemon.TLS.Enabled {
		return "https"
	}
	return "http"
}

func (s *Server) serveDashboard(w http.ResponseWriter, r *http.Request) {
	routes := s.table.List()
	tld := s.cfg.Daemon.TLD
	port := s.cfg.Daemon.Port
	scheme := s.vibeScheme()

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

	viewMode := s.cfg.Dashboard.View
	if viewMode == "" {
		viewMode = "list"
	}
	listBtnClass, gridBtnClass := "active", ""
	if viewMode == "grid" {
		listBtnClass, gridBtnClass = "", "active"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<!DOCTYPE html>
<html lang="en">
<head>
%s
<style>%s

/* Nav bar */
.navbar{
  display:flex;align-items:center;justify-content:space-between;
  padding:0 24px;height:52px;background:var(--surface);
  border-bottom:1px solid var(--border);position:sticky;top:0;z-index:100;
}
.navbar-left{display:flex;align-items:center;gap:16px}
.brand{font-family:var(--font-display);font-size:1.15rem;color:var(--amber);letter-spacing:.5px;text-decoration:none}
.status-pill{display:flex;align-items:center;gap:8px;font-size:.78rem;color:var(--text-secondary);letter-spacing:.04em;text-transform:uppercase}

/* Main content */
.main{max-width:960px;margin:0 auto;padding:28px 24px 60px}

/* Toolbar */
.toolbar{display:flex;align-items:center;justify-content:space-between;margin-bottom:20px}
.toolbar-left{display:flex;align-items:baseline;gap:10px}
.toolbar-title{font-family:var(--font-display);font-size:1.1rem;color:var(--text);letter-spacing:.08em;text-transform:uppercase}
.toolbar-count{font-size:.78rem;color:var(--text-muted);letter-spacing:.04em}
.view-toggle{display:flex;background:var(--elevated);border:1px solid var(--border);border-radius:var(--radius);overflow:hidden}
.view-toggle button{
  font-family:var(--font-body);background:none;border:none;
  color:var(--text-muted);padding:6px 12px;cursor:pointer;
  display:flex;align-items:center;justify-content:center;transition:background .15s,color .15s;
}
.view-toggle button:first-child{border-right:1px solid var(--border)}
.view-toggle button.active{background:var(--hover);color:var(--amber)}
.view-toggle button:hover:not(.active){color:var(--text-secondary)}
.view-toggle svg{width:16px;height:16px}

/* Warning banner */
.warn{
  display:flex;align-items:center;gap:8px;
  padding:12px 16px;margin-bottom:20px;
  font-size:.82rem;color:var(--yellow);
  background:rgba(212,168,42,.06);
  border:1px solid rgba(212,168,42,.2);border-radius:var(--radius);
}

/* Route table (list view) */
.list-view{width:100%%}
.route-table{width:100%%;border-collapse:collapse}
.route-table thead th{
  font-size:.7rem;text-transform:uppercase;letter-spacing:.1em;
  color:var(--text-muted);font-weight:500;text-align:left;
  padding:0 12px 10px;border-bottom:1px solid var(--border);
}
.route-table tbody tr{border-bottom:1px solid var(--border-subtle);transition:background .1s}
.route-table tbody tr:hover{background:var(--elevated)}
.route-table td{padding:11px 12px;vertical-align:middle}
.route-name-cell{display:flex;align-items:center;gap:10px}
.route-icon{
  width:28px;height:28px;border-radius:7px;
  display:flex;align-items:center;justify-content:center;
  font-size:15px;line-height:1;flex-shrink:0;
  background:var(--elevated);border:1px solid var(--border-subtle);
}
.route-icon-stopped{opacity:.35}
.route-name-link{color:var(--text);text-decoration:none;font-weight:600;font-size:.92rem;transition:color .15s}
.route-name-link:hover{color:var(--amber)}
.route-url{color:var(--text-muted);font-size:.75rem;margin-left:4px}
.td-type{font-size:.75rem;text-transform:uppercase;letter-spacing:.08em;font-weight:500;color:var(--text-muted)}
.td-port{font-variant-numeric:tabular-nums;font-size:.85rem}
.td-port a{color:var(--text-secondary);text-decoration:none;transition:color .15s}
.td-port a:hover{color:var(--amber)}
.td-age{color:var(--text-muted);font-size:.8rem}
.td-actions{display:flex;align-items:center;gap:6px;justify-content:flex-end}
.td-stopped{opacity:.45}
.hide-mobile{}

/* Grid view */
.grid-view{display:none}
.grid-container{
  display:grid;
  grid-template-columns:repeat(auto-fill,minmax(150px,1fr));
  gap:16px;
}
.grid-tile{
  background:var(--surface);border:1px solid var(--border-subtle);
  border-radius:var(--radius-lg);padding:20px 12px 16px;
  display:flex;flex-direction:column;align-items:center;text-align:center;
  cursor:pointer;transition:background .15s,border-color .15s,box-shadow .2s;
  position:relative;text-decoration:none;color:var(--text);
}
.grid-tile:hover{background:var(--elevated);border-color:var(--border);box-shadow:0 2px 16px rgba(0,0,0,.3)}
.grid-tile:hover .tile-hover-action{opacity:1}
.tile-icon{
  width:64px;height:64px;border-radius:16px;
  display:flex;align-items:center;justify-content:center;
  font-size:30px;margin-bottom:10px;
  background:var(--elevated);border:1px solid var(--border);
  flex-shrink:0;
}
.tile-stopped .tile-icon{opacity:.35}
.tile-name{font-weight:600;font-size:.88rem;color:var(--text);margin-bottom:3px;letter-spacing:.02em}
.tile-url{font-size:.7rem;color:var(--text-muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:100%%}
.tile-hover-action{position:absolute;top:8px;right:8px;opacity:0;transition:opacity .15s}
.tile-hover-action .btn{font-size:.65rem;padding:3px 8px}

/* Empty state */
.empty{text-align:center;padding:48px 0;color:var(--text-muted);font-size:.88rem}

/* Get Started section */
.get-started{
  margin-top:36px;background:var(--surface);
  border:1px solid var(--border);border-radius:var(--radius-lg);padding:24px;
}
.get-started h2{
  font-family:var(--font-display);font-size:.95rem;
  letter-spacing:.1em;text-transform:uppercase;color:var(--amber);margin-bottom:6px;
}
.get-started p{font-size:.82rem;color:var(--text-secondary);margin-bottom:14px;line-height:1.5}
.setup-box{position:relative}
.setup-textarea{
  width:100%%;font-family:var(--font-body);font-size:.8rem;
  background:var(--bg);color:var(--text-secondary);
  border:1px solid var(--border);border-radius:var(--radius);
  padding:14px;resize:none;line-height:1.6;
}
.btn-copy{
  position:absolute;top:10px;right:10px;
  font-family:var(--font-body);font-size:.7rem;font-weight:600;
  text-transform:uppercase;letter-spacing:.06em;
  background:var(--elevated);color:var(--text-secondary);
  border:1px solid var(--border);padding:5px 12px;
  border-radius:var(--radius);cursor:pointer;transition:all .15s;
}
.btn-copy:hover{color:var(--text);border-color:var(--text-muted)}

/* Icon picker */
.icon-picker{display:flex;flex-wrap:wrap;gap:6px;margin-top:8px}
.icon-pick{
  width:36px;height:36px;border-radius:8px;border:1px solid var(--border);
  background:var(--bg);display:flex;align-items:center;justify-content:center;
  font-size:18px;cursor:pointer;transition:all .12s;
}
.icon-pick:hover{border-color:var(--amber);background:var(--elevated)}
.icon-pick.selected{border-color:var(--amber);background:var(--elevated);box-shadow:0 0 8px var(--amber-glow)}

/* Footer */
.footer{
  max-width:960px;margin:0 auto;padding:0 24px 32px;
  display:flex;align-items:center;justify-content:center;
  gap:20px;font-size:.72rem;color:var(--text-muted);
}
.footer a{color:var(--text-muted);text-decoration:none;transition:color .15s}
.footer a:hover{color:var(--amber)}
.footer-sep{opacity:.3}

@media(max-width:700px){
  .navbar{padding:0 16px}
  .main{padding:20px 16px 48px}
  .grid-container{grid-template-columns:repeat(auto-fill,minmax(130px,1fr));gap:12px}
  .hide-mobile{display:none}
}
@media(max-width:480px){
  .grid-container{grid-template-columns:repeat(2,1fr)}
}
</style>
</head>
<body>
`, themeHead("local.vibe"), themeCSS)

	// Nav bar
	uptime := time.Since(s.startedAt).Round(time.Second)
	fmt.Fprintf(&b, `<nav class="navbar">
  <div class="navbar-left">
    <a href="%s://local.%s/" class="brand">local.vibe</a>
    <div class="status-pill"><span class="led led-green"></span>Running &middot; %s</div>
  </div>
  <button class="btn-add" onclick="openAddModal()">+ Add Route</button>
</nav>
<main class="main">
`, scheme, tld, fmtDuration(uptime))

	// Warning banner
	if unknownName != "" {
		fmt.Fprintf(&b, `<div class="warn"><span class="led led-yellow"></span>No route for <strong style="color:var(--text);margin:0 4px">%s.%s</strong> — register it below.</div>`,
			html.EscapeString(unknownName), tld)
	}

	// Toolbar with view toggle
	fmt.Fprintf(&b, `<div class="toolbar">
  <div class="toolbar-left">
    <span class="toolbar-title">Routes</span>
    <span class="toolbar-count">%d active</span>
  </div>
  <div class="view-toggle">
    <button id="btn-list" class="%s" onclick="setView('list',true)" title="List view">
      <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><line x1="1" y1="3" x2="15" y2="3"/><line x1="1" y1="8" x2="15" y2="8"/><line x1="1" y1="13" x2="15" y2="13"/></svg>
    </button>
    <button id="btn-grid" class="%s" onclick="setView('grid',true)" title="Grid view">
      <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><rect x="1" y="1" width="5.5" height="5.5" rx="1"/><rect x="9.5" y="1" width="5.5" height="5.5" rx="1"/><rect x="1" y="9.5" width="5.5" height="5.5" rx="1"/><rect x="9.5" y="9.5" width="5.5" height="5.5" rx="1"/></svg>
    </button>
  </div>
</div>
`, len(routes)+1, listBtnClass, gridBtnClass)

	// Pool of visually distinct icons assigned by name hash so each route
	// gets a consistent, unique-looking icon out of the box.
	iconPool := []string{
		"🚀", "🤖", "📦", "⚡", "🔮", "🎯", "🛠️", "💎",
		"🧪", "🌐", "📡", "🎨", "🔒", "🗂️", "📊", "🧩",
	}

	// ── LIST VIEW ──
	listDisplay := "block"
	if viewMode == "grid" {
		listDisplay = "none"
	}
	fmt.Fprintf(&b, `<div class="list-view" id="list-view" style="display:%s">
<table class="route-table">`, listDisplay)
	b.WriteString(`
<thead><tr>
  <th style="width:38%%">Name</th>
  <th>Type</th>
  <th>Port</th>
  <th class="hide-mobile">Age</th>
  <th style="text-align:right">Actions</th>
</tr></thead>
<tbody>`)

	// Self row
	fmt.Fprintf(&b, `<tr>
  <td><div class="route-name-cell"><span class="route-icon">&#127968;</span><div><a href="%[4]s://local.%[1]s/" class="route-name-link">local</a><span class="route-url">local.%[1]s</span></div></div></td>
  <td class="td-type">daemon</td>
  <td class="td-port"><a href="http://localhost:%[2]d" target="_blank">%[2]d</a></td>
  <td class="td-age hide-mobile">%[3]s</td>
  <td></td>
</tr>`, tld, port, fmtDuration(uptime), scheme)

	// Route rows
	for _, r := range routes {
		safeName := html.EscapeString(r.Name)
		vibeURL := fmt.Sprintf("%s://%s.%s", scheme, safeName, tld)

		isStopped := r.Type == RouteManaged && !s.isPortReady(r.Port) && !r.Running.Load()

		// Secondary URL display
		urlDisplay := fmt.Sprintf("%s.%s", safeName, tld)
		if r.Type == RouteBookmark {
			urlDisplay = html.EscapeString(r.ExternalURL)
		}

		// Type label (plain text, no color)
		typeLabel := string(r.Type)

		// Port — link to localhost:port, or dash for bookmarks
		portCell := fmt.Sprintf(`<a href="http://localhost:%d" target="_blank">%d</a>`, r.Port, r.Port)
		if r.Type == RouteBookmark {
			portCell = "—"
		}

		// Age
		since := time.Since(r.RegisteredAt).Round(time.Second)

		// Action buttons
		actionHTML := ""
		editSVG := `<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M11.5 1.5l3 3L5 14H2v-3L11.5 1.5z"/></svg>`
		if r.Type == RouteManaged && s.isPortReady(r.Port) {
			actionHTML = fmt.Sprintf(`<button class="btn" onclick="routeAction(this,'%s','stop')">Stop</button>`, safeName)
		} else if r.Type == RouteManaged {
			actionHTML = fmt.Sprintf(`<button class="btn btn-primary" onclick="routeAction(this,'%s','start')">Start</button>`, safeName)
		}

		// Edit button — pass user icon and auto icon separately so "Clear" reverts to auto.
		editBtn := ""
		safeIcon := html.EscapeString(r.Icon)
		safeAutoIcon := html.EscapeString(r.AutoIcon)
		if r.Type == RouteManaged {
			editBtn = fmt.Sprintf(`<button class="btn-icon" onclick="openManagedModal('%s',%d,'%s','%s')" title="Settings">%s</button>`, safeName, r.IdleTimeout, safeIcon, safeAutoIcon, editSVG)
		} else if r.Type == RouteSticky || r.Type == RouteBookmark {
			safeExtURL := html.EscapeString(r.ExternalURL)
			editType := "local"
			if r.Type == RouteBookmark {
				editType = "bookmark"
			}
			editBtn = fmt.Sprintf(`<button class="btn-icon" onclick="openEditModal('%s',%d,'%s','%s','%s','%s')" title="Edit">%s</button>`,
				safeName, r.Port, safeExtURL, editType, safeIcon, safeAutoIcon, editSVG)
		}

		portDimStyle := ""
		if isStopped {
			portDimStyle = ` class="td-stopped"`
		}

		// Icon — user override > auto-detected > deterministic pool pick
		icon := iconPool[nameHash(r.Name)%len(iconPool)]
		if r.AutoIcon != "" {
			icon = html.EscapeString(r.AutoIcon)
		}
		if r.Icon != "" {
			icon = html.EscapeString(r.Icon)
		}

		iconClass := "route-icon"
		if isStopped {
			iconClass = "route-icon route-icon-stopped"
		}

		fmt.Fprintf(&b, `<tr>
  <td><div class="route-name-cell"><span class="%s">%s</span><div><a href="%s" target="_blank" class="route-name-link">%s</a><span class="route-url">%s</span></div></div></td>
  <td class="td-type">%s</td>
  <td class="td-port"%s>%s</td>
  <td class="td-age hide-mobile">%s</td>
  <td><div class="td-actions">%s%s</div></td>
</tr>`, iconClass, iconHTML(icon), vibeURL, safeName, urlDisplay,
			typeLabel, portDimStyle, portCell, fmtDuration(since), actionHTML, editBtn)
	}

	if len(routes) == 0 {
		b.WriteString(`</tbody></table>
<div class="empty">No routes registered yet.</div>
</div>`)
	} else {
		b.WriteString(`</tbody></table>
</div>`)
	}

	// ── GRID VIEW ──
	gridDisplay := "none"
	if viewMode == "grid" {
		gridDisplay = "block"
	}
	fmt.Fprintf(&b, `<div class="grid-view" id="grid-view" style="display:%s"><div class="grid-container">`, gridDisplay)

	// Self tile
	fmt.Fprintf(&b, `<a href="%s://local.%s/" class="grid-tile">
  <div class="tile-icon"><span>&#127968;</span></div>
  <div class="tile-name">local</div>
  <div class="tile-url">local.%s</div>
</a>`, scheme, tld, tld)

	// Route tiles
	for _, r := range routes {
		safeName := html.EscapeString(r.Name)
		vibeURL := fmt.Sprintf("%s://%s.%s", scheme, safeName, tld)

		// URL display
		urlDisplay := fmt.Sprintf("%s.%s", safeName, tld)
		if r.Type == RouteBookmark {
			urlDisplay = html.EscapeString(r.ExternalURL)
		}

		// Icon — user override > auto-detected > deterministic pool pick
		icon := iconPool[nameHash(r.Name)%len(iconPool)]
		if r.AutoIcon != "" {
			icon = html.EscapeString(r.AutoIcon)
		}
		if r.Icon != "" {
			icon = html.EscapeString(r.Icon)
		}

		// Stopped state — dim icon only, not text
		isStopped := r.Type == RouteManaged && !s.isPortReady(r.Port) && !r.Running.Load()
		tileClass := "grid-tile"
		if isStopped {
			tileClass = "grid-tile tile-stopped"
		}

		fmt.Fprintf(&b, `<a href="%s" target="_blank" class="%s">
  <div class="tile-icon">%s</div>
  <div class="tile-name">%s</div>
  <div class="tile-url">%s</div>
</a>`, vibeURL, tileClass, iconHTML(icon), safeName, urlDisplay)
	}

	b.WriteString(`</div></div>`)

	// Get Started section
	fmt.Fprintf(&b, `
<section class="get-started">
  <h2>Get Started</h2>
  <p>Paste this into Claude Code, Cursor, or any agentic IDE to configure a project with local.vibe.</p>
  <div class="setup-box">
    <textarea id="setup-text" class="setup-textarea" rows="5" readonly>This machine is running local.vibe, a local DNS tool that gives dev servers friendly .%[1]s names instead of hard-to-remember port numbers.

Read the full setup instructions:

  curl http://localhost:7999/setup.md

Or open in a browser: %[2]s://local.%[1]s/setup.md</textarea>
    <button class="btn-copy" id="copy-btn" onclick="copyText()">Copy</button>
  </div>
</section>
</main>

<!-- Add/Edit Modal -->
<div class="modal-overlay" id="modal-overlay" onclick="if(event.target===this)closeModal()">
<div class="modal">
  <h3 id="modal-title">Add Route</h3>
  <div class="type-toggle" id="type-toggle">
    <button id="toggle-local" class="active" onclick="setRouteType('local')">Local Port</button>
    <button id="toggle-bookmark" onclick="setRouteType('bookmark')">External URL</button>
  </div>
  <div style="margin-bottom:16px">
    <label for="route-name">Name</label>
    <input id="route-name" type="text" placeholder="myapp" autocomplete="off">
  </div>
  <div id="port-field" style="margin-bottom:16px">
    <label for="route-port">Port</label>
    <input id="route-port" type="number" placeholder="3000">
  </div>
  <div id="url-field" style="display:none;margin-bottom:16px">
    <label for="route-url">URL</label>
    <input id="route-url" type="text" placeholder="example.com:8080">
  </div>
  <div style="margin-bottom:16px">
    <label>Icon</label>
    <input id="route-icon" type="hidden">
    <div class="icon-select-row">
      <div class="icon-preview icon-preview-lg" id="icon-preview">?</div>
      <input id="route-icon-custom" type="text" class="icon-custom-input" placeholder="Type emoji" oninput="onCustomIcon('route-icon','icon-preview','icon-clear','icon-picker')">
      <button type="button" class="btn btn-sm" id="icon-clear" onclick="clearIcon('route-icon','icon-preview','icon-clear','icon-picker')" style="display:none">Clear</button>
    </div>
    <div class="icon-picker" id="icon-picker"></div>
  </div>
  <div class="modal-actions">
    <button class="btn btn-danger" id="modal-delete" style="display:none;margin-right:auto" onclick="deleteRoute()">Delete</button>
    <button class="btn" onclick="closeModal()">Cancel</button>
    <button class="btn-add" id="modal-save" onclick="saveRoute()">Add</button>
  </div>
</div>
</div>
<!-- Managed Route Settings Modal -->
<div class="modal-overlay" id="managed-overlay" onclick="if(event.target===this)closeManagedModal()">
<div class="modal">
  <h3>Route Settings</h3>
  <div style="margin-bottom:16px">
    <label for="managed-name">Name</label>
    <input id="managed-name" type="text" placeholder="myapp" autocomplete="off">
  </div>
  <div style="margin-bottom:16px">
    <label>Icon</label>
    <input id="managed-icon" type="hidden">
    <div class="icon-select-row">
      <div class="icon-preview icon-preview-lg" id="managed-icon-preview">?</div>
      <input id="managed-icon-custom" type="text" class="icon-custom-input" placeholder="Type emoji" oninput="onCustomIcon('managed-icon','managed-icon-preview','managed-icon-clear','managed-icon-picker')">
      <button type="button" class="btn btn-sm" id="managed-icon-clear" onclick="clearIcon('managed-icon','managed-icon-preview','managed-icon-clear','managed-icon-picker')" style="display:none">Clear</button>
    </div>
    <div class="icon-picker" id="managed-icon-picker"></div>
  </div>
  <label for="idle-timeout">Auto-stop after idle (minutes)</label>
  <input id="idle-timeout" type="number" min="0" placeholder="0 = never">
  <p class="hint">Stop the process automatically when no traffic is received. Set to 0 to disable.</p>
  <div class="modal-actions">
    <button class="btn btn-danger" style="margin-right:auto" onclick="deleteManagedRoute()">Delete</button>
    <button class="btn" onclick="closeManagedModal()">Cancel</button>
    <button class="btn-add" onclick="saveManagedSettings()">Save</button>
  </div>
</div>
</div>
<script>
// View toggle
function setView(mode,save){
  var l=document.getElementById('list-view'),g=document.getElementById('grid-view');
  var bl=document.getElementById('btn-list'),bg=document.getElementById('btn-grid');
  if(mode==='grid'){l.style.display='none';g.style.display='block';bl.classList.remove('active');bg.classList.add('active')}
  else{l.style.display='block';g.style.display='none';bl.classList.add('active');bg.classList.remove('active')}
  if(save)fetch('/_api/preferences',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({view:mode})});
}

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
  setIconField('route-icon','icon-preview','icon-clear','icon-picker','');
  setRouteType('local');
  document.getElementById('modal-overlay').classList.add('active');
}
function openEditModal(name,port,url,type,icon,autoIcon){
  modalMode='edit';editingName=name;
  document.getElementById('modal-title').textContent='Edit Route';
  document.getElementById('modal-save').textContent='Save';
  document.getElementById('modal-delete').style.display='';
  document.getElementById('type-toggle').style.display='';
  document.getElementById('route-name').value=name;
  document.getElementById('route-name').disabled=false;
  document.getElementById('route-icon').dataset.autoicon=autoIcon||'';
  setIconField('route-icon','icon-preview','icon-clear','icon-picker',icon||'',autoIcon||'');
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
  var icon=getIconValue('route-icon');
  if(icon)body.icon=icon;
  if(isBookmark){
    var u=document.getElementById('route-url').value.trim();
    if(!u)return;
    if(u.indexOf('://')===-1)u='http://'+u;
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
  var btn=document.getElementById('copy-btn');
  if(navigator.clipboard&&window.isSecureContext){
    navigator.clipboard.writeText(ta.value).then(function(){
      btn.textContent='Copied';setTimeout(function(){btn.textContent='Copy'},1500);
    });
  }else{
    ta.select();document.execCommand('copy');window.getSelection().removeAllRanges();
    btn.textContent='Copied';setTimeout(function(){btn.textContent='Copy'},1500);
  }
}
var managedEditName='';
function openManagedModal(name,idle,icon,autoIcon){
  managedEditName=name;
  document.getElementById('managed-name').value=name;
  document.getElementById('idle-timeout').value=idle||'';
  document.getElementById('managed-icon').dataset.autoicon=autoIcon||'';
  setIconField('managed-icon','managed-icon-preview','managed-icon-clear','managed-icon-picker',icon||'',autoIcon||'');
  document.getElementById('managed-overlay').classList.add('active');
}
function closeManagedModal(){document.getElementById('managed-overlay').classList.remove('active')}
function deleteManagedRoute(){
  if(!managedEditName)return;
  fetch('/_api/routes/'+encodeURIComponent(managedEditName),{method:'DELETE',headers:{'Accept':'application/json'}}).then(function(){location.reload()});
}
function saveManagedSettings(){
  var name=document.getElementById('managed-name').value.trim();
  if(!name)return;
  var idle=parseInt(document.getElementById('idle-timeout').value)||0;
  var icon=getIconValue('managed-icon');
  var body={name:name,idle_timeout:idle};
  if(icon)body.icon=icon;
  fetch('/_api/routes/'+encodeURIComponent(managedEditName),{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}).then(function(){location.reload()});
}
function setIconField(inputId,previewId,clearId,pickerId,val,autoIcon){
  var inp=document.getElementById(inputId);
  inp.value=val||'';
  if(autoIcon!==undefined)inp.dataset.autoicon=autoIcon;
  var display=val||inp.dataset.autoicon||'';
  setIconPreviewEl(document.getElementById(previewId),display);
  document.getElementById(clearId).style.display=val?'':'none';
  var customInp=document.getElementById(inputId+'-custom');
  if(customInp&&!val)customInp.value='';
  highlightPicker(pickerId,val||'');
}
function getIconValue(inputId){return document.getElementById(inputId).value}
function clearIcon(inputId,previewId,clearId,pickerId){
  var autoIcon=document.getElementById(inputId).dataset.autoicon||'';
  document.getElementById(inputId+'-custom').value='';
  setIconField(inputId,previewId,clearId,pickerId,'',autoIcon);
}
function onCustomIcon(inputId,previewId,clearId,pickerId){
  var val=document.getElementById(inputId+'-custom').value.trim();
  setIconField(inputId,previewId,clearId,pickerId,val);
}
var iconChoices=['🚀','🤖','📦','⚡','🔮','🎯','🛠️','💎','🧪','🌐','📡','🎨','🔒','🗂️','📊','🧩'];
function buildPicker(containerId,inputId,previewId,clearId){
  var c=document.getElementById(containerId);
  c.innerHTML='';
  iconChoices.forEach(function(em){
    var b=document.createElement('button');
    b.type='button';b.className='icon-pick';b.textContent=em;
    b.onclick=function(){
      setIconField(inputId,previewId,clearId,containerId,em);
    };
    c.appendChild(b);
  });
}
function highlightPicker(containerId,val){
  var btns=document.getElementById(containerId).querySelectorAll('.icon-pick');
  btns.forEach(function(b){b.classList.toggle('selected',b.textContent===val)});
}
buildPicker('icon-picker','route-icon','icon-preview','icon-clear');
buildPicker('managed-icon-picker','managed-icon','managed-icon-preview','managed-icon-clear');

function setIconPreviewEl(el,val){
  if(!val){el.textContent='?';el.style.backgroundImage='';return}
  if(val.startsWith('data:')||val.startsWith('http://')||val.startsWith('https://')){
    el.textContent='';el.style.backgroundImage='url('+val+')';
    el.style.backgroundSize='cover';el.style.backgroundPosition='center';
  }else{
    el.style.backgroundImage='';el.textContent=val;
  }
}
function routeAction(btn,name,action){
  var origText=btn.textContent;
  btn.disabled=true;
  btn.innerHTML='<span class="spinner"></span>';
  fetch('/_api/routes/'+encodeURIComponent(name)+'/'+action,{method:'POST',headers:{'Accept':'application/json'}})
    .then(function(r){
      if(!r.ok)return r.json().then(function(d){throw new Error(d.error||'Request failed')});
      return r.json();
    })
    .then(function(){
      if(action==='start'){pollReady(name,0)}
      else{pollStopped(name,0)}
    })
    .catch(function(e){
      btn.disabled=false;btn.textContent=origText;
      showToast(e.message||'Action failed');
    });
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
      else if(d.running===false){showToast(name+' crashed during startup');location.reload()}
      else{setTimeout(function(){pollReady(name,n+1)},500)}
    })
    .catch(function(){setTimeout(function(){pollReady(name,n+1)},500)});
}
function showToast(msg){
  var el=document.createElement('div');el.className='toast';el.textContent=msg;
  document.body.appendChild(el);
  setTimeout(function(){el.classList.add('show')},10);
  setTimeout(function(){el.classList.remove('show');setTimeout(function(){el.remove()},300)},4000);
}
document.addEventListener('keydown',function(e){if(e.key==='Escape'){closeModal();closeManagedModal()}});
document.addEventListener('visibilitychange',function(){if(!document.hidden){location.reload()}});
</script>
`, tld, scheme)

	// Footer
	fmt.Fprintf(&b, `<footer class="footer">
  <span>daemon :%d</span>
  <span class="footer-sep">|</span>
  <a href="/_api/routes">API</a>
  <span class="footer-sep">|</span>
  <a href="/_api/health">Health</a>
</footer>
`, port)

	b.WriteString(`</body></html>`)
	fmt.Fprint(w, b.String())
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// nameHash returns a stable hash for a route name, used to pick a default icon.
func nameHash(name string) int {
	h := 0
	for _, c := range name {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return h
}

// iconHTML returns the icon as either an emoji span or an <img> tag for URLs/data URIs.
func iconHTML(icon string) string {
	if strings.HasPrefix(icon, "data:") || strings.HasPrefix(icon, "http://") || strings.HasPrefix(icon, "https://") {
		return fmt.Sprintf(`<img src="%s" style="width:100%%;height:100%%;object-fit:contain;border-radius:inherit" onerror="this.style.display='none';this.parentElement.textContent='🔲'">`, html.EscapeString(icon))
	}
	return icon
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
