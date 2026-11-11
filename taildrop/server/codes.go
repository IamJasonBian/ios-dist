package main

import "fmt"

// Code is one install status. Every banner, attempt response and events.jsonl line uses
// Code.Name; this table is the only place codes are defined.
type Code struct {
	Name    string   // banner/log code: "<state>" or "failed.<reason>"
	Legacy  []string // codes it replaces from the old PROGRESS/ERRORS tables
	Trigger string   // what makes the server (or page) pick it
	Text    string   // banner text; {n} = app name
}

// Codes, in the order an install moves through them. Errors are the failed.* rows.
var Codes = []Code{
	// ── progress ──────────────────────────────────────────────────────────────
	{
		Name:    "installing",
		Legacy:  []string{"PROGRESS.installing"},
		Trigger: "Install tapped or manifest fetched; app.ipa not fully sent yet (each chunk sent resets the idle timer)",
		Text:    "Installing {n}…",
	},
	{
		Name:    "downloaded",
		Legacy:  []string{"PROGRESS.downloaded", "PROGRESS.awaitingLaunch"},
		Trigger: "every byte of app.ipa sent; final for apps without a launch ACK",
		Text:    "{n} downloaded ✓ — open it from the home screen. Icon says “Unable to Install”? This iPhone isn’t in the signing profile (or its iOS is too old): register it, rebuild, resend.",
	},
	{
		Name:    "installed",
		Legacy:  []string{"PROGRESS.launched"},
		Trigger: "app sent GET /api/ack?bundle=&build=<this build>&vid= (sent on every launch)",
		Text:    "{n} installed and opened ✓",
	},

	// ── errors ────────────────────────────────────────────────────────────────
	{
		Name:    "failed.download",
		Legacy:  []string{"ERRORS.neverDownloaded"},
		Trigger: fmt.Sprintf("installing, and no manifest/app.ipa traffic for %v", downloadIdle),
		Text:    "{n} never finished downloading — tap Install in the iOS prompt, or resend the link",
	},
	{
		Name:    "failed.launch",
		Legacy:  []string{"ERRORS.neverLaunched", "ERRORS.unableToInstall"},
		Trigger: fmt.Sprintf("downloaded, app has ack:1, and no launch ACK within %v", launchWait),
		Text:    "{n} downloaded but never opened — if its icon says “Unable to Install”, register this iPhone, rebuild, resend",
	},
	{
		Name:    "failed.unreachable",
		Legacy:  []string{"ERRORS.unreachable"},
		Trigger: "page only: the poll to the server failed, so the server never logs it. A 404 for the attempt (server never saw the tap) shows failed.download",
		Text:    "Install server unreachable — is Tailscale on?",
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
