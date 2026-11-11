---
name: taildrop
description: Build a dev-signed iOS app on the laptop, serve it as an over-the-air itms-services install link over tailnet HTTPS, and Taildrop the link to an iPhone on the tailnet. Use when the user says /taildrop, "beam/send the install to <phone>", "install <app> on <phone> over tailscale", or when devicectl can't install (missing DDI, phone not cabled, phone on hotspot).
---

# taildrop

One command: `~/.claude/skills/taildrop/taildrop.sh <tailnet-device|-> <project-dir> <scheme> <bundle-id> [xcodebuild args...]`

Builds Debug for `generic/platform=iOS` with the laptop ASC key, stamping `CURRENT_PROJECT_VERSION=<yyyymmddHHMM>`; a failed build exits (log in `~/Library/Caches/taildrop-ota/build-<slug>.log`) instead of shipping a stale .app. Packages `app.ipa`, `manifest.plist`, `icon.png`, `meta.json` into `~/Library/Caches/taildrop-ota/<slug>/`.

**All installs live on one page:** `https://<this-mac>.tail70b2c1.ts.net:8443/ota/` (tailnet-only `tailscale serve` → `ota_server.py` on 127.0.0.1:8799). It lists every published app (icon, version, build, Install). Old `/ota/<slug>/` links redirect there. The script Taildrops `Installs link.txt` to the device; device `-` = publish only.

**Install status on the phone:** after tapping Install, the page polls `/ota/api/events` and shows a banner: red if the server is unreachable or the IPA isn't downloaded within 45 s; green once the phone has downloaded it. Progress and error messages are separate tables (`PROGRESS` / `ERRORS` in `ota_server.py`). iOS never reports install success or failure to a server, so once the IPA is downloaded the page shows a “Unable to Install”? button; tapping it shows the fix (UDID not in the signing profile, or iOS too old). Every banner ends with its code — the table key, e.g. `[ERRORS.unreachable]` — and every `ERRORS.*` banner is logged to `events.jsonl` under that code (`grep ERRORS ~/Library/Caches/taildrop-ota/events.jsonl`; `ERRORS.unableToInstall` means register the phone, rebuild, resend). `ERRORS.unreachable` usually can't reach the server, so it rarely appears in the log. An app can opt into launch confirmation by GETting `/ota/api/launch/<slug>` on first launch and setting `"checkin": 1` in its meta.json; then the banner turns red if no launch arrives within 3 min. Neither app does this yet.

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
On the phone: accept the Taildrop (Files → Tailscale), open the link in **Safari** with Tailscale on, tap Install; if "Untrusted Developer", trust it in Settings → General → VPN & Device Management. Link lives only while this laptop is awake and the :8799 server runs. Tear down: `tailscale serve --https=8443 off; pkill -f ota_server.py`. Event log: `~/Library/Caches/taildrop-ota/events.jsonl`.

## Gotchas
- `tailscale serve` of a directory under the Claude scratchpad 403s (tailscaled can't read it) — hence the localhost proxy.
- Xcode reuses stale cached team profiles that lack newer phones; the script parks profiles older than a day into `~/pp-old` so a fresh one is fetched.
- iPhone 18 Pro (chip 0x8160) has no DDI in Xcode 27.2, so devicectl can't install there — this OTA path is the workaround. Unverified whether OTA dev builds launch on it.
- Taildrop only reaches devices on the laptop's *active* tailnet. Phones are on `tail70b2c1` (`tailscale switch 28bd`); ask before switching.
