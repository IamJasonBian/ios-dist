package main

// On-site registration for new iPhones. The page serves a Profile Service .mobileconfig;
// installing it makes iOS POST the phone's UDID to /api/enroll. The phone is then
// "pending" until the build computer registers it with the team (ascreg), rebuilds with
// a profile that includes it, and marks it "ready" via /api/devices/<udid>?state=.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// Device is one phone that asked to be registered from the page.
type Device struct {
	UDID    string  `json:"udid"`
	Name    string  `json:"name"`
	Product string  `json:"product"` // e.g. iPhone16,1
	Version string  `json:"version"` // iOS version
	State   string  `json:"state"`   // see DeviceStates in codes.go
	Build   string  `json:"build"`   // first build that includes it (state ready)
	T       float64 `json:"t"`       // last change
}

var (
	udidRe   = regexp.MustCompile(`^[0-9A-Fa-f-]{25,40}$`)
	plistKey = func(k string) *regexp.Regexp {
		return regexp.MustCompile(`<key>` + k + `</key>\s*<string>([^<]*)</string>`)
	}
	reUDID, reProduct, reVersion, reName = plistKey("UDID"), plistKey("PRODUCT"), plistKey("VERSION"), plistKey("DEVICE_NAME")
)

// enrollProfile is the Profile Service payload; iOS posts the listed attributes to URL.
const enrollProfile = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>PayloadContent</key><dict>
  <key>URL</key><string>%s/api/enroll</string>
  <key>DeviceAttributes</key><array><string>UDID</string><string>PRODUCT</string><string>VERSION</string><string>DEVICE_NAME</string></array>
</dict>
<key>PayloadOrganization</key><string>taildrop</string>
<key>PayloadDisplayName</key><string>Register this iPhone for test installs</string>
<key>PayloadDescription</key><string>Sends this iPhone's UDID to the install server so the next build can include it. Installs nothing.</string>
<key>PayloadIdentifier</key><string>dist.taildrop.enroll</string>
<key>PayloadUUID</key><string>6F1E2B7C-4D3A-4E8B-9C2F-7A5D1E3B9C40</string>
<key>PayloadType</key><string>Profile Service</string>
<key>PayloadVersion</key><integer>1</integer>
</dict></plist>
`

func (s *server) enrollConfig(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	w.Header().Set("Content-Disposition", `attachment; filename="register.mobileconfig"`)
	fmt.Fprintf(w, enrollProfile, s.base)
}

// enroll takes iOS's signed reply (PKCS#7 around a plist), records the phone as pending
// and sends Safari back to the page, which then shows this phone's state.
func (s *server) enroll(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	get := func(re *regexp.Regexp) string {
		if m := re.FindSubmatch(body); m != nil {
			return string(m[1])
		}
		return ""
	}
	udid := get(reUDID)
	if !udidRe.MatchString(udid) {
		http.Error(w, "no UDID in enrollment", 400)
		return
	}
	s.mu.Lock()
	d := s.devices[udid]
	if d == nil {
		d = &Device{UDID: udid, State: "pending"}
		s.devices[udid] = d
	}
	d.Name, d.Product, d.Version, d.T = get(reName), get(reProduct), get(reVersion), unix(s.now())
	s.saveDevices()
	s.mu.Unlock()
	http.Redirect(w, r, s.base+"/?udid="+udid, http.StatusMovedPermanently) // iOS opens this in Safari
}

// devicesAPI lists phones (GET /api/devices) or moves one to a new state
// (GET /api/devices/<udid>?state=registered|ready&build=), as the build computer reports.
func (s *server) devicesAPI(w http.ResponseWriter, r *http.Request, udid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if udid != "" {
		d := s.devices[udid]
		state := r.URL.Query().Get("state")
		if d == nil || DeviceStates[state] == "" {
			http.Error(w, "unknown device or state", 400)
			return
		}
		d.State, d.T = state, unix(s.now())
		if b := r.URL.Query().Get("build"); b != "" {
			d.Build = b
		}
		s.saveDevices()
	}
	rows := make([]*Device, 0, len(s.devices))
	for _, d := range s.devices {
		rows = append(rows, d)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].T > rows[j].T })
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rows)
}

// deviceState returns the page view of one phone, or nil. Caller must not hold s.mu.
func (s *server) deviceState(udid string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devices[udid]
	if d == nil {
		return nil
	}
	return map[string]string{"state": d.State, "build": d.Build, "text": DeviceStates[d.State]}
}

// saveDevices writes devices.json atomically. Caller holds s.mu.
func (s *server) saveDevices() {
	b, _ := json.MarshalIndent(s.devices, "", " ")
	tmp := filepath.Join(s.root, "devices.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, filepath.Join(s.root, "devices.json"))
	}
}

func (s *server) loadDevices() {
	if b, err := os.ReadFile(filepath.Join(s.root, "devices.json")); err == nil {
		json.Unmarshal(b, &s.devices)
	}
}
