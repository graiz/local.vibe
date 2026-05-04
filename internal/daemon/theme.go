package daemon

import "fmt"

// themeHead returns the shared <head> preamble: charset, viewport, title, and fonts.
func themeHead(title string) string {
	return fmt.Sprintf(`<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600;700&family=Share+Tech+Mono&display=swap" rel="stylesheet">`, title)
}

// themeCSS is the shared base stylesheet — retro-futurist command center design.
// Embedded into html/template via template.CSS(themeCSS), so % is literal.
const themeCSS = `
:root{
  --bg:#111113;
  --surface:#19191d;
  --elevated:#222228;
  --hover:#2a2a32;
  --border:#2e2e38;
  --border-subtle:#252530;
  --text:#e8e4de;
  --text-secondary:#908a7e;
  --text-muted:#605c54;
  --amber:#e8943a;
  --amber-dim:#c07828;
  --amber-glow:rgba(232,148,58,.25);
  --green:#3ecf5a;
  --green-dim:#2a9e40;
  --green-glow:rgba(62,207,90,.35);
  --red:#e54545;
  --red-dim:#b83030;
  --red-glow:rgba(229,69,69,.35);
  --yellow:#d4a82a;
  --yellow-dim:#a88520;
  --yellow-glow:rgba(212,168,42,.35);
  --font-body:'IBM Plex Mono',monospace;
  --font-display:'Share Tech Mono',monospace;
  --radius:6px;
  --radius-lg:10px;
}
*{box-sizing:border-box;margin:0;padding:0}
html{font-size:14px}
body{
  font-family:var(--font-body);
  background:var(--bg);
  color:var(--text);
  line-height:1.5;
  min-height:100vh;
  -webkit-font-smoothing:antialiased;
}
a{color:var(--text);text-decoration:none}
a:hover{color:var(--amber)}

/* Buttons */
.btn{
  font-family:var(--font-body);font-size:.72rem;font-weight:600;
  text-transform:uppercase;letter-spacing:.06em;
  padding:4px 10px;border-radius:var(--radius);cursor:pointer;
  border:1px solid var(--border);transition:all .15s;
  display:inline-flex;align-items:center;justify-content:center;gap:6px;
  background:var(--elevated);color:var(--text-secondary);
}
.btn:hover{background:var(--hover);color:var(--text);border-color:var(--text-muted)}
.btn-primary{background:rgba(62,207,90,.1);color:var(--green);border-color:rgba(62,207,90,.25)}
.btn-primary:hover{background:rgba(62,207,90,.2);border-color:var(--green);box-shadow:0 0 8px var(--green-glow)}
.btn-danger{background:transparent;color:var(--red);border-color:rgba(229,69,69,.25)}
.btn-danger:hover{background:rgba(229,69,69,.1);border-color:var(--red)}
.btn:disabled{opacity:.4;cursor:not-allowed;pointer-events:none}
.btn-add{
  font-family:var(--font-body);font-size:.8rem;font-weight:600;
  letter-spacing:.06em;text-transform:uppercase;
  background:var(--amber);color:var(--bg);border:none;
  padding:7px 16px;border-radius:var(--radius);cursor:pointer;
  transition:background .15s,box-shadow .15s;
}
.btn-add:hover{background:#f0a048;box-shadow:0 0 12px var(--amber-glow)}

/* Spinner */
@keyframes spin{to{transform:rotate(360deg)}}
.spinner{display:inline-block;width:12px;height:12px;border:1.5px solid #333;border-top-color:#888;border-radius:50%;animation:spin .6s linear infinite}

/* LED indicators */
.led{width:9px;height:9px;border-radius:50%;flex-shrink:0;display:inline-block;position:relative}
.led::after{content:'';position:absolute;inset:-3px;border-radius:50%;opacity:.6;filter:blur(3px)}
.led-green{background:var(--green);box-shadow:0 0 4px var(--green),0 0 8px var(--green-glow)}
.led-green::after{background:var(--green-glow)}
.led-red{background:var(--red);box-shadow:0 0 4px var(--red),0 0 8px var(--red-glow)}
.led-red::after{background:var(--red-glow)}
.led-yellow{background:var(--yellow);box-shadow:0 0 4px var(--yellow),0 0 8px var(--yellow-glow)}
.led-yellow::after{background:var(--yellow-glow)}
.led-gray{background:#4a4a52}

/* Modal */
.modal-overlay{display:none;position:fixed;inset:0;background:rgba(6,6,10,.15);backdrop-filter:blur(8px);z-index:200;align-items:center;justify-content:center;padding:24px}
.modal-overlay.active{display:flex}
.modal{
  position:relative;
  background:#1c1c22;
  border:1px solid var(--border);
  border-radius:14px;width:460px;max-width:100%;max-height:calc(100vh - 48px);
  overflow:hidden;display:flex;flex-direction:column;
  box-shadow:0 24px 64px -8px rgba(0,0,0,.65),0 0 0 1px rgba(255,255,255,.02) inset;
  animation:modal-in .22s cubic-bezier(.2,.8,.2,1);
}
.modal::before{
  content:'';position:absolute;top:0;left:0;right:0;height:1px;
  background:linear-gradient(90deg,transparent,rgba(232,148,58,.4) 30%,rgba(232,148,58,.4) 70%,transparent);
  pointer-events:none;
}
@keyframes modal-in{from{opacity:0;transform:translateY(12px) scale(.96)}to{opacity:1;transform:translateY(0) scale(1)}}

.modal-header{
  padding:22px 28px 20px;flex-shrink:0;
  display:flex;align-items:center;gap:18px;
  border-bottom:1px solid var(--border-subtle);
}
.modal-header-text{flex:1;min-width:0;display:flex;flex-direction:column;justify-content:center;gap:2px}
.modal-eyebrow{
  display:flex;align-items:center;gap:7px;
  font-family:var(--font-display);font-size:.66rem;
  letter-spacing:.16em;text-transform:uppercase;color:var(--amber);
}
.modal-eyebrow::before{
  content:'';width:5px;height:5px;border-radius:50%;
  background:var(--amber);box-shadow:0 0 6px var(--amber-glow);
}
.modal h3{
  font-family:var(--font-display);font-size:1.3rem;
  letter-spacing:.02em;color:var(--text);
  text-transform:none;line-height:1.1;
  white-space:nowrap;overflow:hidden;text-overflow:ellipsis;
}
.modal-sub{font-size:.74rem;color:var(--text-muted);letter-spacing:.02em;line-height:1.4}

/* Header icon — clickable, opens picker popover */
.icon-trigger{
  width:56px;height:56px;border-radius:12px;flex-shrink:0;
  background:linear-gradient(180deg,#22222a,#1a1a20);
  border:1px solid var(--border);cursor:pointer;
  display:flex;align-items:center;justify-content:center;
  font-size:30px;line-height:1;position:relative;
  background-size:cover;background-position:center;
  transition:border-color .15s,box-shadow .15s,transform .12s;
}
.icon-trigger:hover{border-color:var(--amber);box-shadow:0 0 0 3px rgba(232,148,58,.1)}
.icon-trigger:active{transform:scale(.96)}

/* Icon popover — anchored under the header icon */
.icon-popover{
  position:absolute;top:84px;left:28px;z-index:10;
  width:320px;padding:14px;
  background:#1c1c22;border:1px solid var(--border);
  border-radius:12px;
  box-shadow:0 12px 32px rgba(0,0,0,.55),0 0 0 1px rgba(255,255,255,.02) inset;
  display:none;
  animation:popover-in .15s ease;
}
.icon-popover.active{display:block}
@keyframes popover-in{from{opacity:0;transform:translateY(-4px) scale(.98)}to{opacity:1;transform:translateY(0) scale(1)}}
.icon-popover .icon-popover-row{display:flex;gap:8px;margin-bottom:10px}
.icon-popover input{
  flex:1;width:auto!important;background:rgba(0,0,0,.35);border:1px solid var(--border);
  border-radius:8px;padding:8px 10px;color:var(--text);
  font-family:var(--font-body);font-size:.9rem;outline:none;
}
.icon-popover input:focus{border-color:var(--amber)}
.icon-popover .btn-sm{font-size:.7rem;padding:0 12px}

.modal-body{padding:22px 28px;overflow-y:auto;flex:1;min-height:0}
.modal-body::-webkit-scrollbar{width:6px}
.modal-body::-webkit-scrollbar-thumb{background:var(--border);border-radius:3px}

.field{margin-bottom:18px}
.field:last-child{margin-bottom:0}
.field-row{display:flex;gap:12px;margin-bottom:18px}
.field-row .field{flex:1;margin-bottom:0;min-width:0}
.field-row .field-port{flex:0 0 120px}
.modal label{display:block;font-size:.66rem;font-weight:600;text-transform:uppercase;letter-spacing:.14em;color:var(--text-muted);margin-bottom:8px}
.modal input,.modal select{
  display:block;box-sizing:border-box;
  width:100%;min-width:0;
  background:rgba(0,0,0,.35);border:1px solid var(--border);
  border-radius:8px;padding:11px 13px;color:var(--text);
  font-family:var(--font-body);font-size:.9rem;outline:none;
  transition:border-color .15s,background .15s,box-shadow .15s;
}
.modal input:hover{border-color:var(--text-muted)}
.modal input:focus,.modal select:focus{border-color:var(--amber);background:rgba(0,0,0,.5);box-shadow:0 0 0 3px rgba(232,148,58,.12)}
.modal input::placeholder{color:#4a4a52}

.modal .hint{color:var(--text-muted);font-size:.76rem;margin-top:6px;line-height:1.55}
.modal .hint code{background:rgba(232,148,58,.08);padding:1px 6px;border-radius:4px;font-family:var(--font-body);color:var(--amber);font-size:.74rem;border:1px solid rgba(232,148,58,.15)}

/* Options card — groups checkbox-style toggles with subtle container */
.options-card{
  background:rgba(0,0,0,.22);border:1px solid var(--border-subtle);
  border-radius:10px;padding:4px 14px;margin-bottom:18px;
}
.options-card .opt{padding:12px 0;border-bottom:1px solid var(--border-subtle)}
.options-card .opt:last-child{border-bottom:none}
.modal label.checkbox-row{
  display:flex;align-items:center;gap:10px;
  font-size:.88rem;font-weight:500;text-transform:none;letter-spacing:0;
  color:var(--text);margin:0;cursor:pointer;
}
.modal label.checkbox-row input[type=checkbox]{
  width:16px;height:16px;margin:0;accent-color:var(--amber);flex:none;cursor:pointer;
}
.options-card .hint{margin-left:26px;margin-top:4px;font-size:.74rem}

/* Type toggle — refined segmented control with amber underline */
.type-toggle{
  display:flex;background:rgba(0,0,0,.3);border:1px solid var(--border);
  border-radius:8px;padding:3px;margin-bottom:20px;position:relative;
}
.type-toggle button{
  flex:1;padding:8px 12px;background:none;border:none;border-radius:6px;
  font-family:var(--font-body);font-size:.74rem;font-weight:600;
  text-transform:uppercase;letter-spacing:.1em;
  color:var(--text-muted);cursor:pointer;transition:all .18s;
  position:relative;
}
.type-toggle button:hover:not(.active){color:var(--text-secondary)}
.type-toggle button.active{
  background:linear-gradient(180deg,rgba(232,148,58,.14),rgba(232,148,58,.06));
  color:var(--amber);
  box-shadow:0 1px 0 rgba(255,255,255,.04) inset,0 0 0 1px rgba(232,148,58,.25);
}

/* Icon picker — large preview tile + inline custom input */
.icon-select-row{display:flex;align-items:stretch;gap:10px;margin-bottom:12px}
.icon-preview-lg{
  width:54px;height:54px;border-radius:12px;font-size:28px;
  background:linear-gradient(180deg,#22222a,#1a1a20);
  border:1px solid var(--border);flex-shrink:0;
  display:flex;align-items:center;justify-content:center;
  background-size:cover;background-position:center;
  box-shadow:0 1px 0 rgba(255,255,255,.03) inset;
}
.icon-preview{
  width:44px;height:44px;border-radius:10px;
  background:var(--elevated);border:1px solid var(--border);
  display:flex;align-items:center;justify-content:center;
  font-size:22px;flex-shrink:0;background-size:cover;background-position:center;
}
.icon-custom-input{flex:1!important;width:auto!important;text-align:left;font-size:.9rem!important}
.btn-sm{font-size:.7rem;padding:0 12px;height:auto;align-self:stretch}

.icon-picker{
  display:grid;grid-template-columns:repeat(8,1fr);gap:6px;
  padding:10px;background:rgba(0,0,0,.22);
  border:1px solid var(--border-subtle);border-radius:10px;
}
.icon-pick{
  aspect-ratio:1;width:auto;height:auto;
  border-radius:8px;border:1px solid transparent;
  background:var(--elevated);
  display:flex;align-items:center;justify-content:center;
  font-size:18px;cursor:pointer;transition:all .12s;
}
.icon-pick:hover{background:var(--hover);transform:translateY(-1px)}
.icon-pick.selected{
  border-color:var(--amber);background:rgba(232,148,58,.12);
  box-shadow:0 0 0 1px var(--amber),0 0 12px var(--amber-glow);
}

/* Action bar — hairline top border, Delete left as ghost */
.modal-actions{
  display:flex;gap:10px;align-items:center;
  padding:16px 28px;flex-shrink:0;
  border-top:1px solid var(--border-subtle);
  background:rgba(0,0,0,.18);
}
.modal-actions .btn-add{padding:8px 20px}
.modal-actions .btn-danger{
  background:transparent;border-color:transparent;color:var(--text-muted);
  padding:6px 10px;
}
.modal-actions .btn-danger:hover{color:var(--red);background:rgba(229,69,69,.08);border-color:rgba(229,69,69,.2)}

/* Btn icon (edit pencil) */
.btn-icon{
  background:none;border:none;color:var(--text-muted);
  cursor:pointer;padding:4px;border-radius:4px;
  display:inline-flex;align-items:center;transition:color .15s;
}
.btn-icon:hover{color:var(--text)}
.btn-icon svg{width:14px;height:14px}

/* Toast notifications */
.toast{
  position:fixed;bottom:24px;left:50%;transform:translateX(-50%) translateY(20px);
  background:var(--surface);border:1px solid var(--border);color:var(--text);
  padding:12px 20px;border-radius:var(--radius);font-size:.88rem;
  box-shadow:0 8px 24px rgba(0,0,0,.4);opacity:0;transition:opacity .3s,transform .3s;
  z-index:300;max-width:90vw;
}
.toast.show{opacity:1;transform:translateX(-50%) translateY(0)}
`
