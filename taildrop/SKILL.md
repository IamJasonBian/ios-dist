---
name: taildrop
description: Build a dev-signed iOS app on the laptop, serve it as an over-the-air itms-services install link over tailnet HTTPS, and Taildrop the link to an iPhone on the tailnet. Use when the user says /taildrop, "beam/send the install to <phone>", "install <app> on <phone> over tailscale", or when devicectl can't install (missing DDI, phone not cabled, phone on hotspot).
---

# taildrop

One command: `~/.claude/skills/taildrop/taildrop.sh <tailnet-device|-> <project-dir> <scheme> <bundle-id> [xcodebuild args...]`

Builds Debug for `generic/platform=iOS` with the laptop ASC key, stamping `CURRENT_PROJECT_VERSION=<yyyymmddHHMM>`; a failed build exits (log in `~/Library/Caches/taildrop-ota/build-<slug>.log`) instead of shipping a stale .app. Packages `app.ipa`, `icon.png`, `meta.json` and rsyncs them to `great-lakes-ts:~/taildrop-ota/<slug>/` (`TD_SERVER`/`TD_HOST` override).

**All installs live on one page:** `https://great-lakes.tail70b2c1.ts.net:8443/ota/` — tailnet-only `tailscale serve` on great-lakes → `server/main.go` on 127.0.0.1:8799 (binary `~/taildrop/server`, log `~/taildrop/server.log`). The manifest is generated per tap so every download carries an attempt id. The script Taildrops `Installs link.txt` to the device; device `-` = publish only.

**Install status:** each Install tap is one attempt; the server applies the deadlines and logs every state change to `~/taildrop-ota/events.jsonl` on great-lakes, whether or not the phone page stays open. All codes, the old names they replace, and their triggers are in one table: `server/codes.go`. `installing → downloaded → installed`, or `failed.download` (no traffic 45 s), `failed.launch` (ACK app, no launch 3 min), `failed.unreachable` (page-only). `installed` needs the app to include `client/TaildropAck.swift` (GETs `/ota/api/ack?bundle=&build=&vid=` on every launch/foreground) and the build run with `ACK=1`; neither app does yet, so `downloaded` is their final state. Every ACK, any build, updates `~/taildrop-ota/acks.json` (per app + phone: first/last seen, count); the page shows each app's last launch and `/ota/api/status` serves it as JSON for monitors.

## Known apps
| app | dir | scheme | bundle | extra |
|---|---|---|---|---|
| Chord Coach | `~/Desktop/apollo/chord-coach/ios` | `ChordCoach` | `app.greatlakes.chordcoach` | `INFOPLIST_FILE=$HOME/.claude/skills/taildrop/overrides/chordcoach-Info.plist` (server URL = Studio tailnet `http://100.117.72.33:8766/`; CFBundleVersion = `$(CURRENT_PROJECT_VERSION)`; no app icon → script draws a letter tile) |
| Postings | `~/Desktop/apollo/alpha/postings-ios` | `Postings` | `com.trypostings.app` | `-scmProvider system` (private postings-dist) |

## Before running
- `tailscale status | grep iOS` for the device name (phones get renamed; e.g. iphone192 → queensborough-leader, iphone-15-pro → great-lakes-leader).
- The phone's UDID must be registered on team 6R36WQ2LGH. Script prints the profile's UDIDs; pass `UDID=<udid>` to make it fail when missing. Register a new phone with `~/Desktop/apollo/tools/great-lakes-upgrade/register-device.sh`.
- Builds the working tree as-is — say so if the repo has uncommitted changes.

## Tell the user
On the phone: accept the Taildrop (Files → Tailscale), open the link in **Safari** with Tailscale on, tap Install; if "Untrusted Developer", trust it in Settings → General → VPN & Device Management. Server runs under nohup on great-lakes (not yet a LaunchDaemon — gone on reboot). Restart: `ssh great-lakes-ts 'cd ~/taildrop && nohup ./server -root ~/taildrop-ota -base https://great-lakes.tail70b2c1.ts.net:8443/ota >server.log 2>&1 &'`. Deploy: `cd server && go build -o /tmp/server . && scp /tmp/server great-lakes-ts:taildrop/server`.

## Gotchas
- `tailscale serve` of a directory under the Claude scratchpad 403s (tailscaled can't read it) — hence the localhost proxy.
- Xcode reuses stale cached team profiles that lack newer phones; the script parks profiles older than a day into `~/pp-old` so a fresh one is fetched.
- iPhone 18 Pro (chip 0x8160) has no DDI in Xcode 27.2, so devicectl can't install there — this OTA path is the workaround. Unverified whether OTA dev builds launch on it.
- Taildrop only reaches devices on the laptop's *active* tailnet. Phones are on `tail70b2c1` (`tailscale switch 28bd`); ask before switching.
