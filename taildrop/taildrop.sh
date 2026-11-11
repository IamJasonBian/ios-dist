#!/bin/zsh
# Build a dev-signed iOS app on this laptop, publish it to the install server on great-lakes
# (server/main.go behind tailscale serve :8443/ota), and Taildrop the page link to a tailnet iPhone.
#
# Usage: taildrop.sh <tailnet-device|-> <project-dir> <scheme> <bundle-id> [xcodebuild args...]
#   device "-" → build + serve only, no Taildrop
#   UDID=<udid> env → fail if the signed profile lacks that phone
#   ACK=1 env      → app has client/TaildropAck.swift (GET /ota/api/ack on every launch; enables installed/failed.launch)
set -euo pipefail

DEV=$1 PROJ=$2 SCHEME=$3 BUNDLE=$4; shift 4
EXTRA=("$@")
ROOT=~/Library/Caches/taildrop-ota HTTPS=8443
SLUG=${(L)SCHEME}
BUILD=$(date +%Y%m%d%H%M)
SERVER=${TD_SERVER:-great-lakes-ts} REMOTE=taildrop-ota   # ssh host running server/main.go, and its root
HOST=${TD_HOST:-great-lakes.tail70b2c1.ts.net}
BASE=https://$HOST:$HTTPS/ota/$SLUG ALL=https://$HOST:$HTTPS/ota/

ISSUER=$(cat ~/.appstoreconnect/issuer_id)
KEY=$(ls ~/.appstoreconnect/private_keys/AuthKey_*.p8 | head -1)
KID=${${KEY:t:r}#AuthKey_}

echo "▶ build $SCHEME ($BUNDLE)"
# Xcode reuses cached team profiles even when they predate newly registered phones; park them
# so -allowProvisioningUpdates fetches one carrying every registered device.
PP=~/Library/Developer/Xcode/UserData/Provisioning\ Profiles; mkdir -p ~/pp-old
for f in "$PP"/*.mobileprovision(N); do
  p=$(security cms -D -i "$f" 2>/dev/null) || continue
  [[ $p == *"Profile: $BUNDLE<"* || $p == *"Profile: *<"* ]] || continue
  [[ -n ${UDID:-} && $p == *$UDID* ]] && continue
  (( $(date +%s) - $(stat -f %m "$f") < 86400 )) && continue   # fresh (<1 day) profiles stay
  mv "$f" ~/pp-old/
done
cd "$PROJ"
[[ -f project.yml ]] && command -v xcodegen >/dev/null && xcodegen generate -q
rm -rf build/taildrop/Build/Products/Debug-iphoneos   # never ship a previous run's .app
LOG=$ROOT/build-$SLUG.log; mkdir -p $ROOT
if ! xcodebuild -scheme "$SCHEME" -configuration Debug -destination generic/platform=iOS \
  -allowProvisioningUpdates -authenticationKeyPath "$KEY" -authenticationKeyID "$KID" -authenticationKeyIssuerID "$ISSUER" \
  -derivedDataPath build/taildrop -quiet build CURRENT_PROJECT_VERSION=$BUILD "${EXTRA[@]}" >$LOG 2>&1; then
  grep -E 'error:' $LOG | head -20; echo "BUILD FAILED — full log $LOG"; exit 1
fi

APP=""
for a in build/taildrop/Build/Products/Debug-iphoneos/*.app; do
  [[ $(plutil -extract CFBundleIdentifier raw "$a/Info.plist") == "$BUNDLE" ]] && APP=$a
done
[[ -n $APP ]] || { echo "no built .app with bundle id $BUNDLE"; exit 1; }
PROFILE=$(security cms -D -i "$APP/embedded.mobileprovision")
if [[ -n ${UDID:-} && $PROFILE != *$UDID* ]]; then echo "profile lacks $UDID — register the phone first"; exit 1; fi
echo "  $APP — profile devices: $(echo "$PROFILE" | grep -oE '[0-9A-F]{8}-[0-9A-F]{16}' | tr '\n' ' ')"

echo "▶ package"
OUT=$ROOT/$SLUG; rm -rf $OUT; mkdir -p $OUT/pkg/Payload
cp -R "$APP" $OUT/pkg/Payload/
(cd $OUT/pkg && zip -qry ../app.ipa Payload); rm -rf $OUT/pkg
NAME=$(plutil -extract CFBundleDisplayName raw "$APP/Info.plist" 2>/dev/null || plutil -extract CFBundleName raw "$APP/Info.plist")
VER=$(plutil -extract CFBundleShortVersionString raw "$APP/Info.plist")
ICONS=("$APP"/AppIcon60x60@2x.png(N) "$APP"/AppIcon*.png(N))
if (( $#ICONS )); then cp "${ICONS[1]}" $OUT/icon.png
else magick -size 120x120 xc:black -fill white -font "/System/Library/Fonts/Supplemental/Arial Bold.ttf" -pointsize 72 -gravity center -annotate 0 "${NAME[1]}" $OUT/icon.png
fi
python3 -c 'import json,sys,time;print(json.dumps(dict(zip(["slug","name","version","build","bundle"],sys.argv[1:6]),ack=int(sys.argv[6]),built=time.time())))' \
  $SLUG "$NAME" $VER $BUILD $BUNDLE ${ACK:-0} > $OUT/meta.json

echo "▶ publish → $SERVER:$REMOTE/$SLUG (server/main.go, behind tailscale serve :$HTTPS/ota)"
# Self-heal: start the server's restart loop and the tailnet route if either is missing.
ssh $SERVER 'pgrep -f "server -root" >/dev/null || (cd ~/taildrop && nohup ./run.sh >>server.log 2>&1 </dev/null &)
  /usr/local/bin/tailscale --socket=/var/run/tailscaled.socket serve --bg --https='$HTTPS' --set-path /ota http://127.0.0.1:8799 >/dev/null'
# Stage in <slug>.new, then swap dirs, so the page never sees a new ipa with an old meta.json.
ssh $SERVER "rm -rf $REMOTE/$SLUG.new && mkdir -p $REMOTE/$SLUG.new"
rsync -a $OUT/app.ipa $OUT/icon.png $OUT/meta.json $SERVER:$REMOTE/$SLUG.new/
ssh $SERVER "cd $REMOTE && rm -rf $SLUG.old && { [ ! -d $SLUG ] || mv $SLUG $SLUG.old; } && mv $SLUG.new $SLUG && rm -rf $SLUG.old"
for u in $ALL $BASE/icon.png $BASE/manifest.plist; do   # no ?a= → health check, no attempt logged
  code=$(curl -s -o /dev/null -w '%{http_code}' "$u")
  echo "  ${u#https://$HOST:$HTTPS} $code"; [[ $code == 200 ]] || { echo "link not serving — is the server up on $SERVER?"; exit 1; }
done

[[ $DEV == - ]] && { echo "link: $ALL"; exit 0; }
echo "▶ taildrop → $DEV"
NOTE=$ROOT/"Installs link.txt"
printf 'All installs — open in Safari, Tailscale on (%s %s just updated):\n%s\n' "$NAME" "$BUILD" "$ALL" > "$NOTE"
tailscale file cp "$NOTE" "$DEV:"
echo "sent: $ALL"
