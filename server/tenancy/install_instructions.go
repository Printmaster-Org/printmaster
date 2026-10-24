package tenancy

import (
	"html/template"
	"net/http"
	"strings"
)

var installInstructionsTemplate = template.Must(template.New("install-instructions").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>PrintMaster Agent installation</title>
<style nonce="{{.Nonce}}">
:root{color-scheme:light dark;font-family:"Segoe UI",sans-serif;line-height:1.5;background:#f7f9fa;color:#223438}
*{box-sizing:border-box}body{margin:0}main{max-width:760px;margin:0 auto;padding:32px 20px}header{border-bottom:1px solid #bac7ca;padding-bottom:16px}
h1{font-size:24px;margin:8px 0}h2{font-size:18px;margin:24px 0 8px}p{margin:8px 0}dl{display:grid;grid-template-columns:80px minmax(0,1fr);gap:8px;margin:20px 0}dt{font-weight:600}dd{margin:0;overflow-wrap:anywhere}
.notice{border-left:4px solid #b58900;padding:4px 16px;margin:20px 0}.command,pre{padding:14px;background:#e9eff1;border:1px solid #bac7ca;border-radius:6px;white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word;font-family:Consolas,monospace;font-size:13px}
.actions{display:flex;gap:16px;align-items:center;flex-wrap:wrap;margin:12px 0}button{padding:9px 14px;border:1px solid #17647a;border-radius:6px;background:#17647a;color:white;font:inherit;cursor:pointer;min-height:42px}button:disabled{cursor:default;opacity:.7}a{color:#17647a}summary{cursor:pointer;font-weight:600}pre{max-height:480px;overflow:auto}#copy-status{min-height:24px}svg{width:16px;height:16px;vertical-align:-2px;margin-right:7px}
@media(prefers-color-scheme:dark){:root{background:#002b36;color:#d5e0e2}.command,pre{background:#073642;border-color:#526b71}header{border-color:#526b71}a{color:#63bdcf}}
</style></head><body><main>
<header><strong>PrintMaster</strong><h1>Agent installation</h1></header>
<dl><dt>Server</dt><dd>{{.Entry.ServerURL}}</dd><dt>Platform</dt><dd>{{.Entry.Platform}}</dd><dt>Expires</dt><dd>{{.Expires}}</dd></dl>
<section class="notice" aria-label="Before installation">
<p>Run this command only if you trust the Server and expected these instructions. It downloads and executes a script with administrator privileges.</p>
{{if eq .Entry.Platform "windows"}}<p>Use Administrator PowerShell on a machine without an existing Agent. This bootstrap will not update, repair or remove an existing Agent.</p>{{else}}<p>Run in a terminal. Installation uses sudo.</p>{{end}}
<p>Keep this enrollment link private. Do not forward it or include it in screenshots or logs.</p>
{{if .UsesHTTP}}<p><strong>This command uses unencrypted HTTP.</strong> Network interception can replace the script. Use a trusted HTTPS installation path instead.</p>{{end}}
</section>
<h2>Installation command</h2>
<div id="install-command" class="command">{{.Entry.OneLiner}}</div>
<div class="actions"><button id="copy-command" type="button" hidden><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>Copy one-liner</button><a href="{{.DownloadPath}}" download>Download script</a></div>
<p id="copy-status" role="status" aria-live="polite"></p>
<noscript><p>Select and copy the command above, or download the script.</p></noscript>
<details><summary>View full installation script</summary><pre>{{.Entry.Script}}</pre></details>
<p>Initial installation trusts the Server. Subsequent Agent update verification is a separate trust boundary.</p>
</main><script nonce="{{.Nonce}}">
const button = document.getElementById('copy-command');
const command = document.getElementById('install-command');
const status = document.getElementById('copy-status');
button.hidden = false;
button.addEventListener('click', async () => {
    try {
        if (!navigator.clipboard || !window.isSecureContext) throw new Error('Clipboard unavailable');
        await navigator.clipboard.writeText(command.textContent);
        status.textContent = 'Command copied.';
    } catch {
        const range = document.createRange();
        range.selectNodeContents(command);
        const selection = window.getSelection();
        selection.removeAllRanges();
        selection.addRange(range);
        status.textContent = 'Clipboard unavailable. The command is selected for manual copying.';
    }
});
</script></body></html>`))

func serveInstallInstructions(w http.ResponseWriter, r *http.Request, entry installEntry, downloadPath string) {
	nonce := randomHex(16)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	if r.Method == http.MethodHead {
		return
	}
	data := struct {
		Entry        installEntry
		DownloadPath string
		Expires      string
		Nonce        string
		UsesHTTP     bool
	}{entry, downloadPath, entry.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"), nonce, strings.Contains(entry.OneLiner, "http://")}
	if err := installInstructionsTemplate.Execute(w, data); err != nil {
		logError("failed to render installation instructions")
	}
}
