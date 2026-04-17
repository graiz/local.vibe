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
// NOTE: Used inside fmt.Fprintf so literal %% must be %%.
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
.spinner{display:inline-block;width:12px;height:12px;border:1.5px solid #333;border-top-color:#888;border-radius:50%%;animation:spin .6s linear infinite}

/* LED indicators */
.led{width:9px;height:9px;border-radius:50%%;flex-shrink:0;display:inline-block;position:relative}
.led::after{content:'';position:absolute;inset:-3px;border-radius:50%%;opacity:.6;filter:blur(3px)}
.led-green{background:var(--green);box-shadow:0 0 4px var(--green),0 0 8px var(--green-glow)}
.led-green::after{background:var(--green-glow)}
.led-red{background:var(--red);box-shadow:0 0 4px var(--red),0 0 8px var(--red-glow)}
.led-red::after{background:var(--red-glow)}
.led-yellow{background:var(--yellow);box-shadow:0 0 4px var(--yellow),0 0 8px var(--yellow-glow)}
.led-yellow::after{background:var(--yellow-glow)}
.led-gray{background:#4a4a52}

/* Modal */
.modal-overlay{display:none;position:fixed;inset:0;background:rgba(0,0,0,.65);backdrop-filter:blur(4px);z-index:200;align-items:center;justify-content:center}
.modal-overlay.active{display:flex}
.modal{
  background:var(--surface);border:1px solid var(--border);
  border-radius:var(--radius-lg);padding:28px;width:420px;max-width:92vw;
  box-shadow:0 16px 48px rgba(0,0,0,.5);
  animation:modal-in .2s ease;
}
@keyframes modal-in{from{opacity:0;transform:translateY(8px) scale(.97)}to{opacity:1;transform:translateY(0) scale(1)}}
.modal h3{
  font-family:var(--font-display);font-size:1rem;
  letter-spacing:.08em;text-transform:uppercase;
  color:var(--amber);margin-bottom:20px;
}
.modal label{display:block;font-size:.7rem;font-weight:600;text-transform:uppercase;letter-spacing:.1em;color:var(--text-muted);margin-bottom:6px;margin-top:16px}
.modal label:first-of-type{margin-top:0}
.modal input,.modal select{
  width:100%%;background:var(--bg);border:1px solid var(--border);
  border-radius:var(--radius);padding:10px 12px;color:var(--text);
  font-family:var(--font-body);font-size:.88rem;outline:none;
  transition:border-color .15s;
}
.modal input:focus,.modal select:focus{border-color:var(--amber)}
.modal input::placeholder{color:var(--text-muted)}
.modal-actions{display:flex;gap:10px;margin-top:22px;justify-content:flex-end}
.modal .hint{color:var(--text-muted);font-size:.8rem;margin-top:8px;line-height:1.5}

/* Type toggle */
.type-toggle{display:flex;background:var(--bg);border:1px solid var(--border);border-radius:var(--radius);overflow:hidden;margin-bottom:18px}
.type-toggle button{
  flex:1;padding:9px;background:none;border:none;
  font-family:var(--font-body);font-size:.78rem;font-weight:600;
  text-transform:uppercase;letter-spacing:.05em;
  color:var(--text-muted);cursor:pointer;transition:all .15s;
}
.type-toggle button.active{background:var(--elevated);color:var(--amber)}

/* Icon input */
.icon-select-row{display:flex;align-items:center;gap:10px;margin-bottom:8px}
.icon-preview-lg{width:48px;height:48px;font-size:26px}
.icon-custom-input{width:120px!important;flex:none!important;text-align:center;font-size:1.1rem!important}
.btn-sm{font-size:.78rem;padding:4px 12px}
.icon-preview{
  width:44px;height:44px;border-radius:12px;
  background:var(--elevated);border:1px solid var(--border);
  display:flex;align-items:center;justify-content:center;
  font-size:22px;flex-shrink:0;background-size:cover;background-position:center;
}

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
  position:fixed;bottom:24px;left:50%%;transform:translateX(-50%) translateY(20px);
  background:var(--surface);border:1px solid var(--border);color:var(--text);
  padding:12px 20px;border-radius:var(--radius);font-size:.88rem;
  box-shadow:0 8px 24px rgba(0,0,0,.4);opacity:0;transition:opacity .3s,transform .3s;
  z-index:300;max-width:90vw;
}
.toast.show{opacity:1;transform:translateX(-50%) translateY(0)}
`
