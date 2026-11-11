#!/bin/zsh
# Keep the taildrop server up: restart it 2 s after any exit. Used under nohup until the
# LaunchDaemon (com.greatlakes.taildrop.plist) is installed; launchd then runs the binary directly.
cd ${0:A:h}
while true; do
  ./server -root ~/taildrop-ota -addr 127.0.0.1:8799 -base https://great-lakes.tail70b2c1.ts.net:8443/ota
  rc=$?; echo "$(date '+%F %T') server exited $rc — restarting" >&2
  sleep 2
done
