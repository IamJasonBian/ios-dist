package main

import "fmt"

// Code is one install status. Every banner, attempt response and events.jsonl line uses
// Code.Name; this table is the only place codes are defined.
type Code struct {
	Name    string   // banner/log code: "<state>" or "failed.<reason>"
	Legacy  []string // codes it replaces from the old PROGRESS/ERRORS tables
	Trigger string   // what makes the server (or page) pick it
	Fix     string   // what to do about it (docs only; not shown on the page)
	Text    string   // banner text, plain status only; {n} = app name
}

// Codes, in the order an install moves through them. Errors are the failed.* rows.
//
// TODO: first-class path for a phone that isn't in the signing profile (iOS shows "Unable
// to Install" and tells the server nothing). Today it ends as downloaded / failed.launch.
var Codes = []Code{
	// ── progress ──────────────────────────────────────────────────────────────
	{
		Name:    "installing",
		Legacy:  []string{"PROGRESS.installing"},
		Trigger: "Install tapped or manifest fetched; app.ipa not fully sent yet (each chunk sent resets the idle timer)",
		Text:    "Installing {n}",
	},
	{
		Name:    "downloaded",
		Legacy:  []string{"PROGRESS.downloaded", "PROGRESS.awaitingLaunch"},
		Trigger: "every byte of app.ipa sent (also from failed.download, if the user sat on the iOS prompt); final for apps without a launch ACK",
		Text:    "{n} downloaded",
	},
	{
		Name:    "installed",
		Legacy:  []string{"PROGRESS.launched"},
		Trigger: "a phone's first GET /api/ack?bundle=&build=<this build>&vid= (apps ACK every launch; every ACK is a line in server.log)",
		Text:    "{n} installed",
	},

	// ── errors ────────────────────────────────────────────────────────────────
	{
		Name:    "failed.download",
		Legacy:  []string{"ERRORS.neverDownloaded"},
		Trigger: fmt.Sprintf("installing, and no manifest/app.ipa traffic for %v", downloadIdle),
		Fix:     "tap Install in the iOS prompt, or resend the link",
		Text:    "{n} download failed",
	},
	{
		Name:    "failed.launch",
		Legacy:  []string{"ERRORS.neverLaunched", "ERRORS.unableToInstall"},
		Trigger: fmt.Sprintf("downloaded, app has ack:1, and no launch ACK within %v", launchWait),
		Fix:     "open the app",
		Text:    "{n} not opened",
	},
	{
		Name:    "failed.unreachable",
		Legacy:  []string{"ERRORS.unreachable"},
		Trigger: "page only: the poll to the server failed, so the server never logs it. A 404 for the attempt 15 s after the tap (server never saw it) shows failed.download",
		Fix:     "turn Tailscale on; check the store is up",
		Text:    "Server unreachable",
	},
}

// codeText maps Code.Name → Text for the page.
func codeText() map[string]string {
	m := make(map[string]string, len(Codes))
	for _, c := range Codes {
		m[c.Name] = c.Text
	}
	return m
}

// DeviceStates are a new phone's registration states (on-site registration, devices.go),
// in order, with the plain status the page shows on that phone. Separate from install Codes.
var DeviceStates = map[string]string{
	"pending":    "Registration queued",
	"registered": "Registered, building",
	"ready":      "Ready to install",
}
