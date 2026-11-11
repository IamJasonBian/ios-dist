"""Tailnet OTA install server: one page for every app, plus install-progress events.

Serves ~/Library/Caches/taildrop-ota on 127.0.0.1:8799 behind `tailscale serve /ota`.
Each app lives in <root>/<slug>/ with app.ipa, manifest.plist, icon.png and meta.json.
"""

import html
import json
import sys
import time
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(sys.argv[1]).expanduser()
PORT = int(sys.argv[2]) if len(sys.argv) > 2 else 8799
EVENTS = ROOT / "events.jsonl"


def record(slug: str, kind: str) -> None:
    """Append one install-progress event (manifest/ipa fetch, app check-in)."""
    with EVENTS.open("a") as f:
        f.write(json.dumps({"t": time.time(), "slug": slug, "kind": kind}) + "\n")


def events(since: float) -> list[dict]:
    """Return events newer than `since` (epoch seconds)."""
    if not EVENTS.exists():
        return []
    rows = (json.loads(line) for line in EVENTS.read_text().splitlines() if line)
    return [r for r in rows if r["t"] > since]


def apps() -> list[dict]:
    """Return meta.json of every published app, newest build first."""
    metas = [json.loads(p.read_text()) for p in ROOT.glob("*/meta.json")]
    return sorted(metas, key=lambda m: m["built"], reverse=True)


PAGE = """<!doctype html><html><head><meta charset=utf-8>
<meta name=viewport content="width=device-width,initial-scale=1"><title>Installs</title>
<style>
:root{--bg:#fff;--fg:#000;--mut:#666;--line:#ddd;--ok:#0a7d32;--bad:#b00020}
@media (prefers-color-scheme:dark){:root{--bg:#000;--fg:#fff;--mut:#999;--line:#333;--ok:#4cd07d;--bad:#ff5c6c}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px -apple-system,system-ui,sans-serif}
main{max-width:560px;margin:0 auto;padding:24px 16px}
h1{font-size:28px;margin:0 0 16px}
.app{display:flex;gap:14px;align-items:center;padding:14px 0;border-top:1px solid var(--line)}
.app img{width:60px;height:60px;border-radius:14px;border:1px solid var(--line)}
.app div{flex:1;min-width:0}.app b{display:block}.app small{color:var(--mut)}
.app a{background:var(--fg);color:var(--bg);text-decoration:none;padding:8px 16px;border-radius:18px;font-weight:600}
#st{position:sticky;top:0;padding:12px 16px;font-weight:600;display:none}
#st.ok{display:block;background:var(--ok);color:#fff}#st.bad{display:block;background:var(--bad);color:#fff}
#st.run{display:block;background:var(--line)}
#unable{display:block;width:100%;padding:10px 16px;border:0;border-bottom:1px solid var(--line);background:var(--bg);color:var(--bad);font:inherit;font-weight:600;text-align:left}
</style></head><body><div id=st></div><button id=unable hidden>Home-screen icon says “Unable to Install”?</button>
<main><h1>Installs</h1>%ROWS%</main>
<script>
// Progress (where the install is) and errors (what went wrong + the fix) are kept apart.
const PROGRESS={
  installing:n=>'Installing '+n+'…',
  downloaded:n=>n+' downloaded ✓ — iOS is installing it now',
  awaitingLaunch:n=>n+' downloaded — open it from the home screen to confirm',
  launched:n=>n+' installed and opened ✓',
};
const ERRORS={
  unreachable:()=>'Install server unreachable — is Tailscale on and the laptop awake?',
  neverDownloaded:n=>n+' never started downloading — tap Install in the iOS prompt, or the link is stale',
  neverLaunched:n=>n+' downloaded but never opened — check its home-screen icon',
  unableToInstall:n=>'iOS couldn’t install '+n+': this iPhone isn’t in the app’s signing profile (or its iOS is older than the app needs). Register the UDID, rebuild, and send the link again.',
};
const st=document.getElementById('st'),unable=document.getElementById('unable');
function show(cls,msg){st.className=cls;st.textContent=msg;if(cls!=='run'&&navigator.vibrate)navigator.vibrate(200)}
let watch=null,last=null;
document.querySelectorAll('a[data-slug]').forEach(a=>a.addEventListener('click',()=>{
  watch=last={slug:a.dataset.slug,name:a.dataset.name,checkin:a.dataset.checkin==='1',since:Date.now()/1000,got:new Set()};
  unable.hidden=true;show('run',PROGRESS.installing(watch.name));
}));
unable.addEventListener('click',()=>{
  if(!last)return;
  fetch('api/report/'+encodeURIComponent(last.slug)+'/unable').catch(()=>{});
  watch=null;unable.hidden=true;show('bad',ERRORS.unableToInstall(last.name));
});
async function poll(){
  if(!watch)return;
  const r=await fetch('api/events?since='+watch.since).then(r=>r.json()).catch(()=>null);
  if(!r){show('bad',ERRORS.unreachable());watch=null;return}
  r.filter(e=>e.slug===watch.slug).forEach(e=>watch.got.add(e.kind));
  const age=Date.now()/1000-watch.since,n=watch.name;
  if(watch.got.has('launch')){show('ok',PROGRESS.launched(n));watch=null;return}
  if(!watch.got.has('ipa')){if(age>45){show('bad',ERRORS.neverDownloaded(n));watch=null}return}
  unable.hidden=false;  // iOS never reports install failure, so offer the explicit path once the IPA is down
  if(!watch.checkin){show('ok',PROGRESS.downloaded(n));watch=null}
  else if(age>180){show('bad',ERRORS.neverLaunched(n));watch=null}
  else show('run',PROGRESS.awaitingLaunch(n));
}
setInterval(poll,3000);document.addEventListener('visibilitychange',poll);
</script></body></html>"""

ROW = """<section class=app><img src="%(slug)s/icon.png" alt=""><div><b>%(name)s</b>
<small>%(version)s (%(build)s) · built %(when)s</small></div>
<a data-slug="%(slug)s" data-name="%(name)s" data-checkin="%(checkin)s" href="itms-services://?action=download-manifest&amp;url=%(base)s/manifest.plist">Install</a></section>"""


class Handler(SimpleHTTPRequestHandler):
    def __init__(self, *a, **kw):
        super().__init__(*a, directory=str(ROOT), **kw)

    def end_headers(self):
        self.send_header("Cache-Control", "no-store")
        super().end_headers()

    def _send(self, body: str, ctype: str) -> None:
        data = body.encode()
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        path = self.path.split("?")[0]
        if path in ("/", "/index.html"):
            rows = []
            for m in apps():
                when = time.strftime("%b %-d %H:%M", time.localtime(m["built"]))
                rows.append(ROW % {k: html.escape(str(v)) for k, v in {"checkin": 0, **m, "when": when}.items()})
            return self._send(PAGE.replace("%ROWS%", "".join(rows) or "<p>No apps yet.</p>"), "text/html")
        if path == "/api/events":
            since = float(self.path.partition("since=")[2] or 0)
            return self._send(json.dumps(events(since)), "application/json")
        if path.startswith("/api/launch/"):  # optional app check-in on first launch
            record(path.rsplit("/", 1)[1], "launch")
            return self._send("ok", "text/plain")
        if path.startswith("/api/report/") and path.endswith("/unable"):  # phone-reported "Unable to Install"
            record(path.split("/")[3], "unable")
            return self._send("ok", "text/plain")
        slug, _, name = path.strip("/").partition("/")
        if name == "manifest.plist":
            record(slug, "manifest")
        elif name == "app.ipa" and self.command == "GET":
            record(slug, "ipa")
        return super().do_GET()


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
