# ios-dist

Distribution tooling for server to cluster to build ota

```
BUILD-COMPUTE ──build + rsync──► CLUSTER  (Go server, :8443/ota)
   │                                ▲      │
   │ Taildrop link                  │ ACK  │ page + app.ipa
   ▼                                │      ▼
 IPHONE ────────────────────────────┴──────┘
```

Using locally hosted cluster (A30 <-> Mac studio)

  Stand MacBooks for BUILD-COMPUTE, we can move to hosted Sequoia image distributions later

https://great-lakes.tail70b2c1.ts.net:8443/ota/

## taildrop — over-the-air installs over the tailnet

`taildrop/taildrop.sh` builds a dev-signed app on the cluster, publishes it to one tailnet-only install page, and Taildrops that page's link to a phone. No cable, no devicectl, no disk image — this is how apps reach phones whose chip Xcode has no DDI for (iPhone 18 Pro / 0x8160 on Xcode 27.2).

```sh
taildrop/taildrop.sh great-lakes-leader ~/Desktop/apollo/alpha/postings-ios Postings com.trypostings.app -scmProvider system
taildrop/taildrop.sh - <proj> <scheme> <bundle-id>   # publish only, no Taildrop
```

```
 CLUSTER  queensborough-2  (tailnet tail70b2c1, `tailscale switch 28bd`)
 ┌──────────────────────────────────────────────────────────────────┐
 │ taildrop.sh <device> <proj> <scheme> <bundle>                    │
 │   xcodebuild (ASC key, build=yyyymmddHHMM; fail → exit, no stale)│
 │        │                                                         │
 │        ▼                                                         │
 │ ~/Library/Caches/taildrop-ota/<slug>/                            │
 │   app.ipa · manifest.plist · icon.png · meta.json                │
 │        │                                                         │
 │        ▼                                                         │
 │ (old local server; now server/main.go on great-lakes — see below)│
 │   /            all-installs page + status banner JS              │
 │   /api/attempt/<id>  per-tap state · /api/ack  launch ACK        │
 │        ▲                                                         │
 │ tailscale serve  :8443/ota   (HTTPS, tailnet-only)               │
 └────────┬───────────────────────────────────┬─────────────────────┘
          │ 1. tailscale file cp              │ 3. GET manifest + app.ipa
          │    "Installs link.txt"            │    (logged as events)
          ▼                                   │
 PHONE  great-lakes-leader (iPhone 15 Pro)    │
 ┌────────────────────────────────────────────┴─────────────────────┐
 │ 2. Files → Tailscale → link → Safari: /ota/  → tap Install       │
 │ 4. banner polls /api/attempt/<id> every 3s:                      │
 │      red   server unreachable · no download in 45s               │
 │      green app.ipa downloaded                                    │
 │ 5. iOS installs/replaces (same bundle + team → data kept)        │
 │    signature fail → iOS "Unable to Install" (native)             │
 │    [ACK=1] app GETs /api/ack?bundle=&build=&vid= → installed     │
 │ 6. app runtime → backend                                         │
 └──────────────┬───────────────────────────────────────────────────┘
                │ Chord Coach: http://100.117.72.33:8766
                ▼            Postings: app.trypostings.com
 STUDIO  great-lakes  (chord-coach server :8766; no DDI for 0x8160)
```

### What `taildrop.sh` does, step by step

```
 taildrop.sh <device|-> <proj> <scheme> <bundle> [xcodebuild args]
   │
   ├─ 1 prep     park team profiles >1 day old (unless they carry $UDID) → ~/pp-old
   │             xcodegen generate (if project.yml) · rm previous Debug-iphoneos
   ├─ 2 build    xcodebuild Debug, generic/platform=iOS, -allowProvisioningUpdates
   │             ASC key auth · CURRENT_PROJECT_VERSION=yyyymmddHHMM
   │             fail → print error: lines, exit 1   (log: build-<slug>.log)
   ├─ 3 verify   pick the .app whose CFBundleIdentifier = <bundle>
   │             $UDID set but missing from embedded.mobileprovision → exit 1
   ├─ 4 package  Payload/<app> → zip → app.ipa
   │             icon.png (AppIcon or letter tile) · meta.json
   │             into caches/taildrop-ota/<slug>/ (manifest is built per tap by the server)
   ├─ 5 publish  rsync app.ipa, icon.png, then meta.json → great-lakes:~/taildrop-ota/<slug>/
   │             server/main.go behind tailscale serve :8443/ota
   │             /ota/, manifest.plist, icon.png must all return 200
   └─ 6 send     device "-" → print link, exit
                 else tailscale file cp "Installs link.txt" → <device>
```

### Network

```
                         tailnet tail70b2c1  (WireGuard, tailnet-only — no Funnel)
 ┌───────────────────────────────────────────────────────────────────────────────────┐
 │  LAPTOP  queensborough-2                     CLUSTER  great-lakes  100.117.72.33  │
 │  ┌──────────────────────────┐                ┌──────────────────────────────────┐ │
 │  │ taildrop.sh              │   rsync/ssh    │ taildrop server (Go)             │ │
 │  │  xcodebuild + sign       │ ─────────────► │  tailscale serve :8443/ota       │ │
 │  │  package ipa + meta      │  app.ipa,      │  /ota/            install page   │ │
 │  │                          │  meta.json     │  /ota/<slug>/manifest.plist?a=   │ │
 │  │                          │                │  /ota/<slug>/app.ipa?a=          │ │
 │  │                          │                │  /ota/api/ack/<slug>  launch ACK │ │
 │  │                          │                │  deadlines → events.jsonl        │ │
 │  └────────────┬─────────────┘                └───────┬───────────────▲──────────┘ │
 │               │ 1. tailscale file cp                 │ 3. manifest   │ 4. launch  │
 │               │    "Installs link.txt"               │    + app.ipa  │    ACK     │
 │               ▼                                      ▼    (HTTPS)    │            │
 │  IPHONE  great-lakes-leader / queensborough-leader ──────────────────┘            │
 │  ┌──────────────────────────────────────────────────────────────┐                 │
 │  │ 2. Files → Tailscale → link → Safari → Install (mints a=ID)  │                 │
 │  │    page polls /ota/api/attempt/<id>                          │                 │
 │  │ iOS installd ──► Apple (cert/trust check, invisible to us)   │                 │
 │  │ 5. app runtime ──► its backend                               │                 │
 │  └──────────────────────────────────────────────────────────────┘                 │
 └───────────────────────────────────────────────────────────────────────────────────┘
```

Reinstalls replace the app in place (same bundle id + team keep data and sign-in); every build gets a fresh `CFBundleVersion` so installs are distinguishable. Full runbook, known apps and gotchas: [`taildrop/SKILL.md`](taildrop/SKILL.md).

Requirements on the cluster: Xcode, `xcodegen`, ImageMagick, Tailscale CLI on the phones' tailnet, and an App Store Connect API key in `~/.appstoreconnect/` (`issuer_id` + `private_keys/AuthKey_*.p8`).
