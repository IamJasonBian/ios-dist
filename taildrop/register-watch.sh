#!/bin/zsh
# Run on the build computer. When a phone registers on the install page (state pending),
# rebuild every app in apps.conf — taildrop.sh adds the phone to the team and checks each
# profile includes it — then mark the phone ready. --once checks a single time and exits.
set -uo pipefail
HERE=${0:A:h}
ALL=https://${TD_HOST:-great-lakes.tail70b2c1.ts.net}:8443/ota/

state_count() { curl -sf "${ALL}api/devices" | python3 -c "import json,sys;print(sum(d['state']=='$1' for d in json.load(sys.stdin)))" 2>/dev/null || echo 0; }

while true; do
  if (( $(state_count pending) + $(state_count registered) > 0 )); then   # registered = a past build failed; retry
    echo "$(date '+%F %T') ▶ new phone(s) queued — rebuilding all apps"
    ok=1
    while read -r line; do
      [[ -z $line || $line == \#* ]] && continue
      eval "args=($line)"
      $HERE/taildrop.sh - "${args[@]}" </dev/null || { ok=0; echo "  build failed: ${args[2]}"; }
    done < $HERE/apps.conf
    if (( ok )); then
      for u in $(curl -sf "${ALL}api/devices" | python3 -c "import json,sys;[print(d['udid']) for d in json.load(sys.stdin) if d['state']=='registered']"); do
        curl -sf "${ALL}api/devices/$u?state=ready&build=$(date +%Y%m%d%H%M)" >/dev/null && echo "  ready: $u"
      done
    fi
  fi
  [[ ${1:-} == --once ]] && break
  sleep 60
done
