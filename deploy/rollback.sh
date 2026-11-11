#!/bin/sh
# Started by systemd when vordr-agent.service failed (OnFailure, after StartLimitBurst failed starts). Only while a
# self-update is pending (its marker exists) and the previous binary is kept, it puts that one back and starts the
# agent again; the old agent then reports the failed update to the hub. Without a pending update it does nothing - a
# broken agent.yaml stays a failed unit that someone has to look at.
marker=/var/lib/vordr/update.json
bin=/usr/local/bin/vordr-agent
if [ ! -f "$marker" ] || [ ! -f "$bin.old" ]; then
  echo "vordr-agent-rollback: no pending self-update to roll back - nothing done"
  exit 0
fi
mv -f "$bin.old" "$bin"
echo "vordr-agent-rollback: previous binary ($($bin version 2>/dev/null)) back in place, starting the agent again"
systemctl reset-failed vordr-agent.service
systemctl start --no-block vordr-agent.service
