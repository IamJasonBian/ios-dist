// Command server is the tailnet OTA install server: one page for every app, one
// tracked attempt per Install tap, and one final state per attempt in events.jsonl.
//
// Each app lives in <root>/<slug>/ with app.ipa, icon.png and meta.json. The manifest is
// generated per request so every download carries its attempt id (?a=).
//
// Every status code is defined in codes.go.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	downloadIdle = 45 * time.Second // no manifest/ipa bytes for this long → failed.download
	launchWait   = 3 * time.Minute  // downloaded, ACK app, no ACK → failed.launch
	keepAttempts = 24 * time.Hour   // forget attempts older than this
)

var slugRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// Meta is an app's meta.json, written by taildrop.sh.
type Meta struct {
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Build   string  `json:"build"`
	Bundle  string  `json:"bundle"`
	Built   float64 `json:"built"`
	Ack     int     `json:"ack"` // 1 = app GETs /api/ack/<slug>?build= on first launch
}

// Attempt is one Install tap, tracked from tap to its final state.
type Attempt struct {
	ID, Slug, Build, State, Reason string
	Ack                            bool
	Created, Last                  time.Time
	Bytes, Size                    int64
}

// Code returns the banner code for the attempt's current state.
func (a *Attempt) Code() string {
	if a.Reason != "" {
		return a.State + "." + a.Reason
	}
	return a.State
}

// Final reports whether the attempt has reached its last state.
func (a *Attempt) Final() bool {
	return a.State == "installed" || a.State == "failed" || (a.State == "downloaded" && !a.Ack)
}

type server struct {
	root, base string
	mu         sync.Mutex
	attempts   map[string]*Attempt
	logf       *os.File
}

func (s *server) meta(slug string) (Meta, error) {
	var m Meta
	if !slugRe.MatchString(slug) {
		return m, fmt.Errorf("bad slug %q", slug)
	}
	b, err := os.ReadFile(filepath.Join(s.root, slug, "meta.json"))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

func (s *server) apps() []Meta {
	paths, _ := filepath.Glob(filepath.Join(s.root, "*", "meta.json"))
	var out []Meta
	for _, p := range paths {
		if m, err := s.meta(filepath.Base(filepath.Dir(p))); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Built > out[j].Built })
	return out
}

// set moves an attempt to a new state and logs the transition. Caller holds s.mu.
func (s *server) set(a *Attempt, state, reason string) {
	if a.State == state && a.Reason == reason {
		return
	}
	a.State, a.Reason = state, reason
	line, _ := json.Marshal(map[string]any{
		"v": 1, "t": float64(time.Now().UnixMilli()) / 1000, "attempt": a.ID,
		"slug": a.Slug, "build": a.Build, "state": state, "reason": reason, "code": a.Code(),
	})
	s.logf.Write(append(line, '\n'))
}

// attempt returns the attempt with this id, creating it for slug if new. Caller holds s.mu.
func (s *server) attempt(id string, m Meta) *Attempt {
	if a, ok := s.attempts[id]; ok {
		a.Last = time.Now()
		return a
	}
	now := time.Now()
	a := &Attempt{ID: id, Slug: m.Slug, Build: m.Build, Ack: m.Ack == 1, Created: now, Last: now}
	s.attempts[id] = a
	s.set(a, "installing", "")
	return a
}

// sweep applies the deadlines and drops old attempts.
func (s *server) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, a := range s.attempts {
		switch {
		case now.Sub(a.Created) > keepAttempts:
			delete(s.attempts, id)
		case a.State == "installing" && now.Sub(a.Last) > downloadIdle:
			s.set(a, "failed", "download")
		case a.State == "downloaded" && a.Ack && now.Sub(a.Last) > launchWait:
			s.set(a, "failed", "launch")
		}
	}
}

// countWriter counts body bytes sent for an app.ipa download.
type countWriter struct {
	http.ResponseWriter
	n int64
}

func (w *countWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.n += int64(n)
	return n, err
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9]{6,32}$`)

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	id := r.URL.Query().Get("a")
	switch {
	case r.URL.Path == "/" || r.URL.Path == "/index.html":
		s.page(w)
	case len(parts) == 3 && parts[0] == "api" && parts[1] == "start": // tap beacon
		m, err := s.meta(parts[2])
		if err != nil || !idRe.MatchString(id) {
			http.Error(w, "bad request", 400)
			return
		}
		s.mu.Lock()
		s.attempt(id, m)
		s.mu.Unlock()
		fmt.Fprint(w, "ok")
	case len(parts) == 3 && parts[0] == "api" && parts[1] == "attempt":
		s.mu.Lock()
		a, ok := s.attempts[parts[2]]
		var body []byte
		if ok {
			body, _ = json.Marshal(map[string]any{"code": a.Code(), "final": a.Final()})
		}
		s.mu.Unlock()
		if !ok {
			http.Error(w, "unknown attempt", 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	case len(parts) == 3 && parts[0] == "api" && parts[1] == "ack": // app's first launch
		s.ack(w, parts[2], r.URL.Query().Get("build"))
	case len(parts) == 2 && parts[1] == "manifest.plist":
		s.manifest(w, parts[0], id)
	case len(parts) == 2 && parts[1] == "app.ipa":
		s.ipa(w, r, parts[0], id)
	case len(parts) == 2 && parts[1] == "icon.png" && slugRe.MatchString(parts[0]):
		http.ServeFile(w, r, filepath.Join(s.root, parts[0], "icon.png"))
	case len(parts) == 1 && slugRe.MatchString(parts[0]): // old per-app links
		http.Redirect(w, r, "../", http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

func (s *server) ack(w http.ResponseWriter, slug, build string) {
	m, err := s.meta(slug)
	if err != nil {
		http.Error(w, "unknown app", 404)
		return
	}
	if build != m.Build {
		http.Error(w, "stale build "+build+", latest "+m.Build, 409)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var hit *Attempt
	for _, a := range s.attempts {
		if a.Slug == slug && a.Build == build && (a.State == "downloaded" || a.Code() == "failed.launch") &&
			(hit == nil || a.Created.After(hit.Created)) {
			hit = a
		}
	}
	if hit == nil { // e.g. server restarted between download and launch
		hit = &Attempt{ID: "", Slug: slug, Build: build}
	}
	s.set(hit, "installed", "")
	fmt.Fprint(w, "ok")
}

func (s *server) manifest(w http.ResponseWriter, slug, id string) {
	m, err := s.meta(slug)
	if err != nil || !idRe.MatchString(id) {
		http.Error(w, "bad request", 400)
		return
	}
	s.mu.Lock()
	s.attempt(id, m)
	s.mu.Unlock()
	ipa := fmt.Sprintf("%s/%s/app.ipa?a=%s", s.base, slug, id)
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>items</key><array><dict>
<key>assets</key><array><dict><key>kind</key><string>software-package</string><key>url</key><string>%s</string></dict></array>
<key>metadata</key><dict><key>bundle-identifier</key><string>%s</string><key>bundle-version</key><string>%s</string><key>kind</key><string>software</string><key>title</key><string>%s</string></dict>
</dict></array></dict></plist>
`, template.HTMLEscapeString(ipa), template.HTMLEscapeString(m.Bundle), template.HTMLEscapeString(m.Build), template.HTMLEscapeString(m.Name))
}

func (s *server) ipa(w http.ResponseWriter, r *http.Request, slug, id string) {
	m, err := s.meta(slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.root, slug, "app.ipa")
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	cw := &countWriter{ResponseWriter: w}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(cw, r, "app.ipa", st.ModTime(), f)
	if !idRe.MatchString(id) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attempt(id, m)
	a.Bytes += cw.n
	a.Size = st.Size()
	if a.State == "installing" && a.Bytes >= a.Size {
		s.set(a, "downloaded", "")
	}
}

var pageTmpl = template.Must(template.New("page").Funcs(template.FuncMap{
	"when": func(t float64) string { return time.Unix(int64(t), 0).Format("Jan 2 15:04") },
}).Parse(`<!doctype html><html><head><meta charset=utf-8>
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
</style></head><body><div id=st></div>
<main><h1>Installs</h1>
{{range .Apps}}<section class=app><img src="{{.Slug}}/icon.png" alt=""><div><b>{{.Name}}</b>
<small>{{.Version}} ({{.Build}}) · built {{when .Built}}</small></div>
<a data-slug="{{.Slug}}" data-name="{{.Name}}" href="#">Install</a></section>
{{else}}<p>No apps yet.</p>{{end}}</main>
<script>
const CODES={{.Codes}},BASE={{.Base}},st=document.getElementById('st');
let cur=null;
function show(code){
  st.className=code==='installed'||code==='downloaded'?'ok':code.startsWith('failed')?'bad':'run';
  st.textContent=CODES[code].replace('{n}',cur.name)+' ['+code+']';
}
document.querySelectorAll('a[data-slug]').forEach(a=>a.addEventListener('click',()=>{
  const id=Array.from(crypto.getRandomValues(new Uint8Array(8)),b=>b.toString(16).padStart(2,'0')).join('');
  cur={id,name:a.dataset.name};
  fetch('api/start/'+a.dataset.slug+'?a='+id,{keepalive:true}).catch(()=>{});
  a.href='itms-services://?action=download-manifest&url='+encodeURIComponent(BASE+'/'+a.dataset.slug+'/manifest.plist?a='+id);
  show('installing');
}));
async function poll(){
  if(!cur)return;
  const r=await fetch('api/attempt/'+cur.id).then(r=>r.ok?r.json():null).catch(()=>null);
  if(!r){show('failed.unreachable');return}  // page-only: the server can't log what it never heard
  show(r.code);if(r.final)cur=null;
}
setInterval(poll,3000);document.addEventListener('visibilitychange',poll);
</script></body></html>`))

func (s *server) page(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpl.Execute(w, map[string]any{"Apps": s.apps(), "Codes": codeText(), "Base": s.base}); err != nil {
		log.Print(err)
	}
}

func main() {
	root := flag.String("root", "~/taildrop-ota", "directory holding <slug>/{app.ipa,icon.png,meta.json}")
	addr := flag.String("addr", "127.0.0.1:8799", "listen address (behind tailscale serve)")
	base := flag.String("base", "", "public base URL, e.g. https://great-lakes.tail70b2c1.ts.net:8443/ota")
	flag.Parse()
	if *base == "" {
		log.Fatal("-base is required")
	}
	dir := *root
	if strings.HasPrefix(dir, "~/") {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[2:])
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{root: dir, base: strings.TrimRight(*base, "/"), attempts: map[string]*Attempt{}, logf: f}
	go func() {
		for range time.Tick(5 * time.Second) {
			s.sweep()
		}
	}()
	log.Printf("serving %s on %s as %s", dir, *addr, s.base)
	log.Fatal(http.ListenAndServe(*addr, s))
}
