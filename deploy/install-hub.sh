#!/bin/bash
# Installs or updates vordr-hub as a systemd service. Run as root on the hub host:
#   ./install-hub.sh <vordr-hub binary> <hub.yaml> [--replace-config "<reason>"]
# Every check runs before the service is touched; whether the service user can read certificate and key is checked
# by running check-config as that user. An existing /etc/vordr-hub/hub.yaml is only kept or
# replaced by an identical file - unless --replace-config names the reason: then the old file is kept as
# hub.yaml.bak-<time>, the difference is shown and the reason goes to the system log.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
unit="$here/systemd/vordr-hub.service"
fail() { echo "install-hub: $*" >&2; exit 1; }

usage="usage: install-hub.sh <vordr-hub binary> <hub.yaml> [--replace-config \"<reason>\"]"
[ $# -eq 2 ] || { [ $# -eq 4 ] && [ "$3" = "--replace-config" ]; } || fail "$usage"
reason=${4:-}
[ $# -eq 2 ] || [ -n "${reason// }" ] || fail "--replace-config needs a reason"
bin=$(realpath "$1")
cfg=$(realpath "$2")
[ "$(id -u)" = 0 ] || fail "run as root"
[ -d /run/systemd/system ] || fail "systemd is not running on this host"
[ -f "$unit" ] || fail "$unit missing - run the script from the repository's deploy/ folder"
"$bin" version >/dev/null 2>&1 || fail "$bin does not run here (wrong architecture?)"
"$bin" check-config --config "$cfg" || fail "$cfg has issues (listed above) - nothing installed"
if [ -f /etc/vordr-hub/hub.yaml ] && ! cmp -s "$cfg" /etc/vordr-hub/hub.yaml; then
  [ -n "$reason" ] || fail "/etc/vordr-hub/hub.yaml exists and differs from $cfg - compare them and decide yourself, \
or replace it with --replace-config \"<reason>\"; nothing installed"
  diff -u /etc/vordr-hub/hub.yaml "$cfg" || true
  backup=/etc/vordr-hub/hub.yaml.bak-$(date +%Y%m%d-%H%M%S)
  cp -p /etc/vordr-hub/hub.yaml "$backup"
  logger -t install-hub "hub.yaml replaced (old one in $backup): $reason"
  echo "install-hub: hub.yaml will be replaced, old one kept as $backup - reason: $reason"
fi

if ! id -u vordr-hub >/dev/null 2>&1; then
  useradd --system --user-group --no-create-home --home-dir /var/lib/vordr-hub --shell /usr/sbin/nologin vordr-hub
  echo "install-hub: system user vordr-hub created"
fi
install -d -m 0750 -o root -g vordr-hub /etc/vordr-hub
install -m 0640 -o root -g vordr-hub "$cfg" /etc/vordr-hub/hub.yaml.new
# As the service user: can it read hub.yaml and the TLS files?
if ! runuser -u vordr-hub -- "$bin" check-config --config /etc/vordr-hub/hub.yaml.new; then
  rm -f /etc/vordr-hub/hub.yaml.new
  fail "vordr-hub cannot read everything it needs (listed above) - give the group vordr-hub read access"
fi
mv -f /etc/vordr-hub/hub.yaml.new /etc/vordr-hub/hub.yaml

install -m 0755 "$bin" /usr/local/bin/vordr-hub.new
mv -f /usr/local/bin/vordr-hub.new /usr/local/bin/vordr-hub
install -m 0644 "$unit" /etc/systemd/system/vordr-hub.service
systemctl daemon-reload
systemctl enable --quiet vordr-hub.service
systemctl restart vordr-hub.service
sleep 5
if ! systemctl is-active --quiet vordr-hub.service; then
  journalctl -u vordr-hub.service -n 30 --no-pager || true
  fail "the service did not stay up (journal above)"
fi
echo "vordr-hub $(/usr/local/bin/vordr-hub version) runs. Key for every agent.yaml (hub.public_key):"
/usr/local/bin/vordr-hub key --config /etc/vordr-hub/hub.yaml
