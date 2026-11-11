// Command server is the tailnet OTA install server: one page for every app, one tracked
// attempt per Install tap with one final state in events.jsonl, and a per-app record of
// launch ACKs (acks.json) so installed apps' liveness is visible.
//
// Each app lives in <root>/<slug>/ with app.ipa, icon.png and meta.json. The manifest is
// generated per request so every download carries its attempt id (?a=). Every status code
// is defined in codes.go.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	downloadIdle = 45 * time.Second // installing, no manifest/ipa bytes for this long → failed.download
	launchWait   = 3 * time.Minute  // downloaded (ACK app), no ACK for this long → failed.launch
	keepAttempts = 24 * time.Hour   // forget attempts older than this
)

var (
	slugRe = regexp.MustCompile(`^[a-z0-9-]+$`)
	idRe   = regexp.MustCompile(`^[A-Za-z0-9]{6,32}$`)
)

// Meta is an app's meta.json, written by taildrop.sh.
type Meta struct {
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Build   string  `json:"build"`
	Bundle  string  `json:"bundle"`
	Built   float64 `json:"built"`
	Ack     int     `json:"ack"` // 1 = app GETs /api/ack?bundle=&build=&vid= on every launch
}

// Attempt is one Install tap, tracked from tap to its final state.
type Attempt struct {
	ID, Slug, Build, State, Reason string
	Ack                            bool
	Created, Last                  time.Time
	Got, Size                      int64 // contiguous bytes of app.ipa delivered from offset 0
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

// Seen is the launch-ACK record for one app on one phone (vid = identifierForVendor).
type Seen struct {
	Slug  string  `json:"slug"`
	Vid   string  `json:"vid"`
	Build string  `json:"build"`
	First float64 `json:"first"`
	Last  float64 `json:"last"`
	Count int     `json:"count"`
}

type server struct {
	root, base string
	now        func() time.Time
	mu         sync.Mutex
	attempts   map[string]*Attempt
	seen       map[string]*Seen // slug + "/" + vid
	logf       io.Writer
}

func newServer(root, base string, logf io.Writer) *server {
	return &server{root: root, base: strings.TrimRight(base, "/"), now: time.Now,
		attempts: map[string]*Attempt{}, seen: map[string]*Seen{}, logf: logf}
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

// apps returns every published app, newest build first. Staging dirs (slug.new) fail slugRe.
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

func unix(t time.Time) float64 { return float64(t.UnixMilli()) / 1000 }

// set moves an attempt to a new state and logs the transition. Caller holds s.mu.
func (s *server) set(a *Attempt, state, reason string) {
	if a.State == state && a.Reason == reason {
		return
	}
	a.State, a.Reason = state, reason
	line, _ := json.Marshal(map[string]any{
		"v": 1, "t": unix(s.now()), "attempt": a.ID,
		"slug": a.Slug, "build": a.Build, "state": state, "reason": reason, "code": a.Code(),
	})
	s.logf.Write(append(line, '\n'))
}

// attempt returns the attempt with this id, creating it for m if new, and marks activity.
// Caller holds s.mu.
func (s *server) attempt(id string, m Meta) *Attempt {
	now := s.now()
	if a, ok := s.attempts[id]; ok {
		a.Last = now
		return a
	}
	a := &Attempt{ID: id, Slug: m.Slug, Build: m.Build, Ack: m.Ack == 1, Created: now, Last: now}
	s.attempts[id] = a
	s.set(a, "installing", "")
	return a
}

// sweep applies the deadlines and drops old attempts.
func (s *server) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
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

// replay rebuilds recent attempts from events.jsonl so a restart keeps ACK matching and
// deadlines. Restored attempts get a fresh Last, i.e. a full grace period.
func (s *server) replay(r io.Reader) {
	dec := json.NewDecoder(r)
	now := s.now()
	for {
		var e struct {
			T                                   float64
			Attempt, Slug, Build, State, Reason string
		}
		if err := dec.Decode(&e); err != nil {
			break
		}
		t := time.UnixMilli(int64(e.T * 1000))
		if e.Attempt == "" || now.Sub(t) > keepAttempts {
			continue
		}
		a, ok := s.attempts[e.Attempt]
		if !ok {
			m, _ := s.meta(e.Slug)
			a = &Attempt{ID: e.Attempt, Slug: e.Slug, Build: e.Build, Ack: m.Ack == 1, Created: t}
			s.attempts[e.Attempt] = a
		}
		a.State, a.Reason, a.Last = e.State, e.Reason, now
	}
}

// progressWriter reports each chunk of an app.ipa body as it is sent.
type progressWriter struct {
	http.ResponseWriter
	onWrite func(n int)
}

func (w *progressWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if n > 0 {
		w.onWrite(n)
	}
	return n, err
}

// rangeStart returns the first byte offset of a single "bytes=N-…" Range header, else 0.
func rangeStart(h string) int64 {
	spec, ok := strings.CutPrefix(h, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return 0
	}
	n, err := strconv.ParseInt(strings.SplitN(spec, "-", 2)[0], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	q := r.URL.Query()
	id := q.Get("a")
	switch {
	case r.URL.Path == "/" || r.URL.Path == "/index.html":
		s.page(w)
	case r.URL.Path == "/api/ack": // app launch, every launch
		s.ack(w, q.Get("bundle"), q.Get("build"), q.Get("vid"))
	case r.URL.Path == "/api/status":
		s.status(w)
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
	case len(parts) == 2 && parts[1] == "manifest.plist":
		s.manifest(w, parts[0], id)
	case len(parts) == 2 && parts[1] == "app.ipa":
		s.ipa(w, r, parts[0], id)
	case len(parts) == 2 && parts[1] == "icon.png" && slugRe.MatchString(parts[0]):
		http.ServeFile(w, r, filepath.Join(s.root, parts[0], "icon.png"))
	case len(parts) == 1 && slugRe.MatchString(parts[0]): // old per-app links
		http.Redirect(w, r, s.base+"/", http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

// ack records a launch. The first ACK of the current build resolves that build's newest
// open attempt to installed; every ACK, any build, updates acks.json.
func (s *server) ack(w http.ResponseWriter, bundle, build, vid string) {
	var m Meta
	for _, a := range s.apps() {
		if a.Bundle == bundle {
			m = a
		}
	}
	if m.Slug == "" || build == "" {
		http.Error(w, "unknown app", 404)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := unix(s.now())
	key := m.Slug + "/" + vid
	sn := s.seen[key]
	if sn == nil {
		sn = &Seen{Slug: m.Slug, Vid: vid, First: now}
		s.seen[key] = sn
	}
	sn.Build, sn.Last, sn.Count = build, now, sn.Count+1
	s.saveSeen()
	if build != m.Build {
		fmt.Fprint(w, "ok (stale build, latest "+m.Build+")")
		return
	}
	var hit *Attempt
	for _, a := range s.attempts {
		open := !a.Final() || a.Code() == "failed.launch" || a.Code() == "failed.download"
		if a.Slug == m.Slug && a.Build == build && open && (hit == nil || a.Created.After(hit.Created)) {
			hit = a
		}
	}
	if hit != nil {
		s.set(hit, "installed", "")
	}
	fmt.Fprint(w, "ok")
}

// saveSeen writes acks.json atomically. Caller holds s.mu.
func (s *server) saveSeen() {
	rows := make([]*Seen, 0, len(s.seen))
	for _, v := range s.seen {
		rows = append(rows, v)
	}
	b, _ := json.MarshalIndent(rows, "", " ")
	tmp := filepath.Join(s.root, "acks.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, filepath.Join(s.root, "acks.json"))
	}
}

func (s *server) loadSeen() {
	b, err := os.ReadFile(filepath.Join(s.root, "acks.json"))
	if err != nil {
		return
	}
	var rows []*Seen
	json.Unmarshal(b, &rows)
	for _, v := range rows {
		s.seen[v.Slug+"/"+v.Vid] = v
	}
}

// lastSeen returns the newest ACK per slug and how many phones have ever ACKed it.
func (s *server) lastSeen() map[string]map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]map[string]any{}
	for _, v := range s.seen {
		cur := out[v.Slug]
		if cur == nil {
			cur = map[string]any{"last": 0.0, "build": "", "phones": 0}
			out[v.Slug] = cur
		}
		cur["phones"] = cur["phones"].(int) + 1
		if v.Last > cur["last"].(float64) {
			cur["last"], cur["build"] = v.Last, v.Build
		}
	}
	return out
}

// status is a machine-readable view for monitors: each app, its build and its last ACK.
func (s *server) status(w http.ResponseWriter) {
	seen := s.lastSeen()
	var rows []map[string]any
	for _, m := range s.apps() {
		rows = append(rows, map[string]any{"slug": m.Slug, "build": m.Build, "ack": m.Ack == 1, "seen": seen[m.Slug]})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rows)
}

func (s *server) manifest(w http.ResponseWriter, slug, id string) {
	m, err := s.meta(slug)
	if err != nil {
		http.Error(w, "unknown app", 404)
		return
	}
	ipa := fmt.Sprintf("%s/%s/app.ipa", s.base, slug)
	if idRe.MatchString(id) { // no id = health check: serve it, track nothing
		s.mu.Lock()
		s.attempt(id, m)
		s.mu.Unlock()
		ipa += "?a=" + id
	}
	w.Header().Set("Content-Type", "application/xml")
	esc := template.HTMLEscapeString
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>items</key><array><dict>
<key>assets</key><array><dict><key>kind</key><string>software-package</string><key>url</key><string>%s</string></dict></array>
<key>metadata</key><dict><key>bundle-identifier</key><string>%s</string><key>bundle-version</key><string>%s</string><key>kind</key><string>software</string><key>title</key><string>%s</string></dict>
</dict></array></dict></plist>
`, esc(ipa), esc(m.Bundle), esc(m.Build), esc(m.Name))
}

func (s *server) ipa(w http.ResponseWriter, r *http.Request, slug, id string) {
	m, err := s.meta(slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(s.root, slug, "app.ipa"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	if !idRe.MatchString(id) || r.Method == http.MethodHead {
		http.ServeContent(w, r, "app.ipa", st.ModTime(), f)
		return
	}
	s.mu.Lock()
	a := s.attempt(id, m)
	a.Size = st.Size()
	s.mu.Unlock()
	// Bytes count toward Got only while they extend the contiguous prefix from offset 0, so
	// two broken downloads never add up to a "complete" one; each chunk also resets Last.
	off := rangeStart(r.Header.Get("Range"))
	pw := &progressWriter{ResponseWriter: w, onWrite: func(n int) {
		s.mu.Lock()
		defer s.mu.Unlock()
		a.Last = s.now()
		if off <= a.Got {
			a.Got = max(a.Got, off+int64(n))
		}
		off += int64(n)
		if a.State == "installing" && a.Got >= a.Size {
			s.set(a, "downloaded", "")
		}
	}}
	http.ServeContent(pw, r, "app.ipa", st.ModTime(), f)
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
.app div{flex:1;min-width:0}.app b{display:block}.app small{color:var(--mut);display:block}
.app a{background:var(--fg);color:var(--bg);text-decoration:none;padding:8px 16px;border-radius:18px;font-weight:600}
#st{position:sticky;top:0;padding:12px 16px;font-weight:600;display:none}
#st.ok{display:block;background:var(--ok);color:#fff}#st.bad{display:block;background:var(--bad);color:#fff}
#st.run{display:block;background:var(--line)}
</style></head><body><div id=st></div>
<main><h1>Installs</h1>
{{range .Apps}}<section class=app><img src="{{.Slug}}/icon.png" alt=""><div><b>{{.Name}}</b>
<small>{{.Version}} ({{.Build}}) · built {{when .Built}}</small>
<small>{{with index $.Seen .Slug}}last launch {{when .last}} · build {{.build}} · {{.phones}} phone(s){{else}}{{if eq .Ack 1}}no launches yet{{else}}no launch ACK in this app{{end}}{{end}}</small></div>
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
  const r=await fetch('api/attempt/'+cur.id).catch(()=>null);
  if(!r){show('failed.unreachable');return}                      // page-only: the server never heard it
  if(r.status===404){show('failed.download');cur=null;return}    // server never saw the tap or manifest
  const j=await r.json();show(j.code);if(j.final)cur=null;
}
setInterval(poll,3000);document.addEventListener('visibilitychange',poll);
</script></body></html>`))

func (s *server) page(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := map[string]any{"Apps": s.apps(), "Seen": s.lastSeen(), "Codes": codeText(), "Base": s.base}
	if err := pageTmpl.Execute(w, data); err != nil {
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
	events := filepath.Join(dir, "events.jsonl")
	f, err := os.OpenFile(events, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	s := newServer(dir, *base, f)
	if rf, err := os.Open(events); err == nil {
		s.replay(rf)
		rf.Close()
	}
	s.loadSeen()
	go func() {
		for range time.Tick(5 * time.Second) {
			s.sweep()
		}
	}()
	log.Printf("serving %s on %s as %s (%d attempts restored)", dir, *addr, s.base, len(s.attempts))
	log.Fatal(http.ListenAndServe(*addr, s))
}
