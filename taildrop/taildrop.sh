#!/bin/zsh
# Build a dev-signed iOS app on this laptop, serve it as an itms-services OTA link over
# tailnet HTTPS, and Taildrop the link to a tailnet iPhone.
#
# Usage: taildrop.sh <tailnet-device|-> <project-dir> <scheme> <bundle-id> [xcodebuild args...]
#   device "-" → build + serve only, no Taildrop
#   UDID=<udid> env → fail if the signed profile lacks that phone
set -euo pipefail

DEV=$1 PROJ=$2 SCHEME=$3 BUNDLE=$4; shift 4
EXTRA=("$@")
ROOT=~/Library/Caches/taildrop-ota PORT=8799 HTTPS=8443
SLUG=${(L)SCHEME}
BUILD=$(date +%Y%m%d%H%M)
HOST=$(tailscale status --json 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))')
BASE=https://$HOST:$HTTPS/ota/$SLUG

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

echo "▶ package → $BASE/"
OUT=$ROOT/$SLUG; rm -rf $OUT; mkdir -p $OUT/pkg/Payload
cp -R "$APP" $OUT/pkg/Payload/
(cd $OUT/pkg && zip -qry ../app.ipa Payload); rm -rf $OUT/pkg
NAME=$(plutil -extract CFBundleDisplayName raw "$APP/Info.plist" 2>/dev/null || plutil -extract CFBundleName raw "$APP/Info.plist")
VER=$(plutil -extract CFBundleShortVersionString raw "$APP/Info.plist")
cat > $OUT/manifest.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>items</key><array><dict>
<key>assets</key><array><dict><key>kind</key><string>software-package</string><key>url</key><string>$BASE/app.ipa</string></dict></array>
<key>metadata</key><dict><key>bundle-identifier</key><string>$BUNDLE</string><key>bundle-version</key><string>$BUILD</string><key>kind</key><string>software</string><key>title</key><string>$NAME</string></dict>
</dict></array></dict></plist>
EOF
ICONS=("$APP"/AppIcon60x60@2x.png(N) "$APP"/AppIcon*.png(N))
if (( $#ICONS )); then cp "${ICONS[1]}" $OUT/icon.png
else magick -size 120x120 xc:black -fill white -font "/System/Library/Fonts/Supplemental/Arial Bold.ttf" -pointsize 72 -gravity center -annotate 0 "${NAME[1]}" $OUT/icon.png
fi
echo '<meta http-equiv="refresh" content="0;url=../">' > $OUT/index.html   # old per-app links → all-installs page
python3 -c 'import json,sys,time;print(json.dumps(dict(zip(["slug","name","version","build","base","bundle"],sys.argv[1:]),built=time.time())))' \
  $SLUG "$NAME" $VER $BUILD $BASE $BUNDLE > $OUT/meta.json

echo "▶ serve (localhost:$PORT ← tailscale serve :$HTTPS/ota, tailnet only)"
curl -s "http://127.0.0.1:$PORT/api/events?since=9e9" | grep -q '^\[' || {
  pkill -f "http.server $PORT" || true; pkill -f "ota_server.py" || true
  nohup python3 ${0:A:h}/ota_server.py $ROOT $PORT >$ROOT/server.log 2>&1 & sleep 1; }
tailscale serve --bg --https=$HTTPS --set-path /ota http://127.0.0.1:$PORT >/dev/null 2>&1
sleep 1
ALL=https://$HOST:$HTTPS/ota/
for u in $ALL $BASE/manifest.plist $BASE/icon.png; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "$u")
  echo "  ${u#https://$HOST:$HTTPS} $code"; [[ $code == 200 ]] || { echo "link not serving"; exit 1; }
done

[[ $DEV == - ]] && { echo "link: $ALL"; exit 0; }
echo "▶ taildrop → $DEV"
NOTE=$ROOT/"Installs link.txt"
printf 'All installs — open in Safari, Tailscale on (%s %s just updated):\n%s\n' "$NAME" "$BUILD" "$ALL" > "$NOTE"
tailscale file cp "$NOTE" "$DEV:"
echo "sent: $ALL"
