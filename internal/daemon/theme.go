package daemon

import "fmt"

// themeHead returns the shared <head> preamble: charset, viewport, title, and Geist fonts.
func themeHead(title string) string {
	return fmt.Sprintf(`<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Geist:wght@400;500;600&family=Geist+Mono:wght@400;500&display=swap" rel="stylesheet">`, title)
}

// themeCSS is the shared base stylesheet — Vercel-inspired monochrome design.
// NOTE: Used inside fmt.Fprintf so literal %% must be %%.
const themeCSS = `
:root{
  --bg:#000;
  --surface:#0a0a0a;
  --border:#1a1a1a;
  --border-hover:#333;
  --text:#ededed;
  --text-secondary:#888;
  --text-muted:#666;
  --green:#0070f3;
  --green-dot:#52c41a;
  --red-dot:#ff4d4f;
  --yellow-dot:#faad14;
  --blue:#0070f3;
  --font-sans:"Geist","Inter",system-ui,-apple-system,sans-serif;
  --font-mono:"Geist Mono","SF Mono",ui-monospace,monospace;
}
*{box-sizing:border-box;margin:0;padding:0}
body{
  font-family:var(--font-sans);
  background:var(--bg);
  color:var(--text);
  line-height:1.5;
  min-height:100vh;
  -webkit-font-smoothing:antialiased;
}
a{color:var(--text);text-decoration:none}
a:hover{color:#fff}

/* Buttons */
.btn{
  font-family:var(--font-sans);font-size:13px;font-weight:500;
  padding:6px 12px;border-radius:6px;cursor:pointer;
  border:1px solid var(--border);transition:all .15s;
  display:inline-flex;align-items:center;justify-content:center;gap:6px;
  background:var(--bg);color:var(--text-secondary);
}
.btn:hover{border-color:var(--border-hover);color:var(--text)}
.btn-primary{background:#fff;color:#000;border-color:#fff}
.btn-primary:hover{background:#ededed;border-color:#ededed}
.btn-danger{background:transparent;color:#ff4d4f;border-color:#333}
.btn-danger:hover{background:#1a0000;border-color:#ff4d4f}
.btn:disabled{opacity:.5;cursor:not-allowed}

/* Spinner */
@keyframes spin{to{transform:rotate(360deg)}}
.spinner{display:inline-block;width:12px;height:12px;border:1.5px solid #333;border-top-color:#888;border-radius:50%%;animation:spin .6s linear infinite}

/* Modal */
.modal-overlay{display:none;position:fixed;inset:0;background:rgba(0,0,0,.7);z-index:100;align-items:center;justify-content:center}
.modal-overlay.active{display:flex}
.modal{
  background:var(--surface);border:1px solid var(--border);
  border-radius:12px;padding:24px;width:460px;max-width:90vw;
  animation:modal-in .15s ease;
}
@keyframes modal-in{from{opacity:0;transform:translateY(4px)}to{opacity:1;transform:translateY(0)}}
.modal h3{font-size:16px;font-weight:600;color:#fff;margin-bottom:20px}
.modal label{display:block;color:var(--text-muted);font-size:13px;margin-bottom:6px;margin-top:16px}
.modal label:first-of-type{margin-top:0}
.modal input,.modal select{
  width:100%%;background:var(--bg);border:1px solid var(--border);
  border-radius:6px;padding:8px 12px;color:var(--text);
  font-family:var(--font-sans);font-size:14px;outline:none;
  transition:border-color .15s;
}
.modal input:focus,.modal select:focus{border-color:#333}
.modal input::placeholder{color:var(--text-muted)}
.modal-actions{display:flex;gap:8px;margin-top:20px;justify-content:flex-end}
.modal-actions button{
  padding:8px 16px;border-radius:6px;font-size:14px;
  font-family:var(--font-sans);font-weight:500;cursor:pointer;
  transition:all .15s;
}

/* Type toggle */
.type-toggle{display:flex;gap:0;margin-bottom:8px;border:1px solid var(--border);border-radius:6px;overflow:hidden}
.type-toggle button{
  flex:1;padding:8px;background:var(--bg);border:none;
  color:var(--text-muted);font-size:13px;font-family:var(--font-sans);
  cursor:pointer;transition:all .15s;border-right:1px solid var(--border);
}
.type-toggle button:last-child{border-right:none}
.type-toggle button.active{background:var(--surface);color:var(--text)}

/* Status dot */
.dot{width:8px;height:8px;border-radius:50%%;display:inline-block;flex-shrink:0}
.dot-green{background:var(--green-dot)}
.dot-red{background:var(--red-dot)}
.dot-yellow{background:var(--yellow-dot)}
.dot-gray{background:#444}
`
