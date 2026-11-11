package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const ipaSize = 1 << 20

// fixture is a server over one ACK app "demo" (bundle x.demo, build B2) with a fake clock.
type fixture struct {
	s   *server
	log *bytes.Buffer
	clk time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "demo"), 0o755)
	os.WriteFile(filepath.Join(dir, "demo", "app.ipa"), bytes.Repeat([]byte{7}, ipaSize), 0o644)
	os.WriteFile(filepath.Join(dir, "demo", "meta.json"),
		[]byte(`{"slug":"demo","name":"Demo","version":"1","build":"B2","bundle":"x.demo","built":1,"ack":1}`), 0o644)
	f := &fixture{log: &bytes.Buffer{}, clk: time.Unix(1_800_000_000, 0)}
	f.s = newServer(dir, "https://h/ota", f.log)
	f.s.now = func() time.Time { return f.clk }
	return f
}

func (f *fixture) get(path string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	f.s.ServeHTTP(w, r)
	return w
}

func (f *fixture) code(id string) string { return f.s.attempts[id].Code() }

// slowWriter advances the clock 10 s per chunk and runs the deadline sweep mid-download.
type slowWriter struct {
	*httptest.ResponseRecorder
	f *fixture
}

func (w slowWriter) Write(b []byte) (int, error) {
	w.f.clk = w.f.clk.Add(10 * time.Second)
	w.f.s.sweep()
	return w.ResponseRecorder.Write(b)
}

func TestSlowDownloadIsNotFailed(t *testing.T) {
	f := newFixture(t)
	f.get("/demo/manifest.plist?a=slow01")
	r := httptest.NewRequest("GET", "/demo/app.ipa?a=slow01", nil)
	f.s.ServeHTTP(slowWriter{httptest.NewRecorder(), f}, r) // ~32 chunks ≈ 320 s total
	if got := f.code("slow01"); got != "downloaded" {
		t.Fatalf("slow download ended %q, want downloaded; log:\n%s", got, f.log)
	}
}

func TestIdleDownloadFails(t *testing.T) {
	f := newFixture(t)
	f.get("/demo/manifest.plist?a=idle01")
	f.clk = f.clk.Add(downloadIdle + time.Second)
	f.s.sweep()
	if got := f.code("idle01"); got != "failed.download" {
		t.Fatalf("got %q, want failed.download", got)
	}
}

func TestBrokenRetriesDoNotAddUp(t *testing.T) {
	f := newFixture(t)
	f.get("/demo/app.ipa?a=retry1", "Range", "bytes=0-599999")
	f.get("/demo/app.ipa?a=retry1", "Range", "bytes=0-499999")
	if got := f.code("retry1"); got != "installing" {
		t.Fatalf("two partial downloads gave %q, want installing", got)
	}
	f.get("/demo/app.ipa?a=retry1", "Range", "bytes=600000-") // resume from the contiguous end
	if got := f.code("retry1"); got != "downloaded" {
		t.Fatalf("resume gave %q, want downloaded", got)
	}
}

func TestAckResolvesNewestOpenAttemptOnly(t *testing.T) {
	f := newFixture(t)
	f.get("/demo/app.ipa?a=old001") // downloaded
	f.clk = f.clk.Add(time.Minute)
	f.get("/api/start/demo?a=new001") // tapped later, still installing
	if w := f.get("/api/ack?bundle=x.demo&build=B2&vid=V1"); w.Code != 200 {
		t.Fatalf("ack status %d", w.Code)
	}
	if f.code("new001") != "installed" || f.code("old001") != "downloaded" {
		t.Fatalf("new=%s old=%s, want installed/downloaded", f.code("new001"), f.code("old001"))
	}
	if strings.Contains(f.log.String(), `"attempt":""`) {
		t.Fatalf("logged an attempt-less line:\n%s", f.log)
	}
}

func TestAckWithoutAttemptLogsNothing(t *testing.T) {
	f := newFixture(t)
	before := f.log.Len()
	f.get("/api/ack?bundle=x.demo&build=B2&vid=V1")
	if f.log.Len() != before {
		t.Fatalf("ack with no attempt wrote %q", f.log.String()[before:])
	}
	if sn := f.s.seen["demo/V1"]; sn == nil || sn.Count != 1 {
		t.Fatalf("ack not recorded in seen: %+v", sn)
	}
}

func TestStaleBuildAckCountsAsSeenOnly(t *testing.T) {
	f := newFixture(t)
	f.get("/demo/app.ipa?a=cur001")
	f.get("/api/ack?bundle=x.demo&build=B1&vid=V1")
	f.get("/api/ack?bundle=x.demo&build=B1&vid=V1")
	if f.code("cur001") != "downloaded" {
		t.Fatalf("stale-build ack resolved the attempt: %s", f.code("cur001"))
	}
	if sn := f.s.seen["demo/V1"]; sn.Count != 2 || sn.Build != "B1" {
		t.Fatalf("seen = %+v, want count 2 build B1", sn)
	}
}

func TestReplayKeepsAttemptsAcrossRestart(t *testing.T) {
	f := newFixture(t)
	f.get("/demo/app.ipa?a=rest01")
	g := newFixture(t)
	g.s.root = f.s.root
	g.s.replay(bytes.NewReader(f.log.Bytes()))
	if got := g.code("rest01"); got != "downloaded" {
		t.Fatalf("replayed state %q, want downloaded", got)
	}
	g.get("/api/ack?bundle=x.demo&build=B2&vid=V1")
	if got := g.code("rest01"); got != "installed" {
		t.Fatalf("ack after restart gave %q, want installed", got)
	}
}

func TestHealthCheckManifestTracksNothing(t *testing.T) {
	f := newFixture(t)
	w := f.get("/demo/manifest.plist")
	if w.Code != 200 || len(f.s.attempts) != 0 || f.log.Len() != 0 {
		t.Fatalf("status %d, %d attempts, log %q", w.Code, len(f.s.attempts), f.log)
	}
	if strings.Contains(w.Body.String(), "?a=") {
		t.Fatal("health-check manifest carries an attempt id")
	}
}

func TestOldLinkRedirectsToPage(t *testing.T) {
	f := newFixture(t)
	w := f.get("/demo/")
	if loc := w.Header().Get("Location"); w.Code != http.StatusFound || loc != "https://h/ota/" {
		t.Fatalf("redirect %d to %q, want 302 to https://h/ota/", w.Code, loc)
	}
}

func TestLateDownloadRecoversFromFailed(t *testing.T) {
	f := newFixture(t)
	f.get("/demo/manifest.plist?a=late01")
	f.clk = f.clk.Add(downloadIdle + time.Second) // user sat on the iOS install prompt
	f.s.sweep()
	f.get("/demo/app.ipa?a=late01")
	if got := f.code("late01"); got != "downloaded" {
		t.Fatalf("got %q, want downloaded", got)
	}
}

func TestRelaunchOnOtherPhoneDoesNotResolve(t *testing.T) {
	f := newFixture(t)
	f.get("/api/ack?bundle=x.demo&build=B2&vid=PHONEA") // A already runs B2
	f.get("/demo/app.ipa?a=phoneb")                     // B downloads B2
	f.get("/api/ack?bundle=x.demo&build=B2&vid=PHONEA") // A relaunches
	if got := f.code("phoneb"); got != "downloaded" {
		t.Fatalf("A's relaunch resolved B's attempt: %q", got)
	}
	f.get("/api/ack?bundle=x.demo&build=B2&vid=PHONEB")
	if got := f.code("phoneb"); got != "installed" {
		t.Fatalf("B's first launch gave %q, want installed", got)
	}
}

func TestEmptyVidRejected(t *testing.T) {
	f := newFixture(t)
	if w := f.get("/api/ack?bundle=x.demo&build=B2&vid="); w.Code != 400 || len(f.s.seen) != 0 {
		t.Fatalf("status %d, seen %d", w.Code, len(f.s.seen))
	}
}

func TestSuffixAndMultiRangesNotCounted(t *testing.T) {
	f := newFixture(t)
	f.get("/demo/app.ipa?a=suffix", "Range", "bytes=-1048576") // whole file, but as a suffix
	f.get("/demo/app.ipa?a=suffix", "Range", "bytes=0-10,20-1048575")
	if got := f.s.attempts["suffix"]; got.Got != 0 || got.Code() != "installing" {
		t.Fatalf("Got=%d code=%s, want 0/installing", got.Got, got.Code())
	}
}

func TestStaleIfRangeCountsFromZero(t *testing.T) {
	f := newFixture(t)
	f.get("/demo/app.ipa?a=ifrng1", "Range", "bytes=500000-", "If-Range", `"OLDBUILD"`)
	if got := f.code("ifrng1"); got != "downloaded" {
		t.Fatalf("full 200 after stale If-Range gave %q, want downloaded", got)
	}
}

func TestResumeAfterRestart(t *testing.T) {
	f := newFixture(t)
	f.get("/demo/app.ipa?a=resum1", "Range", "bytes=0-499999")
	g := newFixture(t)
	g.s.root = f.s.root
	g.s.replay(bytes.NewReader(f.log.Bytes()))
	g.get("/demo/app.ipa?a=resum1", "Range", "bytes=500000-")
	if got := g.code("resum1"); got != "downloaded" {
		t.Fatalf("resume after restart gave %q, want downloaded", got)
	}
}

func TestReplaySkipsTornLine(t *testing.T) {
	f := newFixture(t)
	logs := `{"t":1800000000,"attempt":"torn01","slug":"demo","build":"B2","state":"insta` + "\n" +
		`{"v":1,"t":1800000000,"attempt":"after1","slug":"demo","build":"B2","state":"downloaded"}` + "\n"
	f.s.replay(strings.NewReader(logs))
	if a := f.s.attempts["after1"]; a == nil || a.State != "downloaded" {
		t.Fatalf("line after a torn line was lost: %+v", a)
	}
}

func TestRepublishMidDownloadConflicts(t *testing.T) {
	f := newFixture(t)
	f.get("/demo/manifest.plist?a=repub1") // attempt pinned to B2
	os.WriteFile(filepath.Join(f.s.root, "demo", "meta.json"),
		[]byte(`{"slug":"demo","name":"Demo","version":"1","build":"B3","bundle":"x.demo","built":2,"ack":1}`), 0o644)
	if w := f.get("/demo/app.ipa?a=repub1"); w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", w.Code)
	}
}
