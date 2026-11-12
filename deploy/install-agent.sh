#!/bin/bash
# Installs or updates vordr-agent on this host as a systemd service. Run as root on the host:
#   ./install-agent.sh <vordr-agent binary for this host> <agent.yaml> [--replace-config "<reason>"]
# Every check runs before anything is changed. An existing /etc/vordr/agent.yaml is only kept or replaced by an
# identical file: changing a host's local policy is a decision, not a side effect of an update. --replace-config
# makes it one: the old file is kept as agent.yaml.bak-<time>, the difference is shown and the reason goes to the
# system log.
# Proxy instances (vordr-agent@<name>, design/proxy-agent.md 4.3) run the same binary. Each running one must accept its
# policy with the new binary before anything changes, and once the new agent stays up they are restarted onto it, as
# the self-update does after its confirmation.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
unit="$here/systemd/vordr-agent.service"
rollback_unit="$here/systemd/vordr-agent-rollback.service"
rollback="$here/rollback.sh"
fail() { echo "install-agent: $*" >&2; exit 1; }

usage="usage: install-agent.sh <vordr-agent binary> <agent.yaml> [--replace-config \"<reason>\"]"
[ $# -eq 2 ] || { [ $# -eq 4 ] && [ "$3" = "--replace-config" ]; } || fail "$usage"
reason=${4:-}
[ $# -eq 2 ] || [ -n "${reason// }" ] || fail "--replace-config needs a reason"
bin=$(realpath "$1")
cfg=$(realpath "$2")
[ "$(id -u)" = 0 ] || fail "run as root (the agent runs as root: apt needs it)"
[ -d /run/systemd/system ] || fail "systemd is not running on this host"
command -v apt-get >/dev/null || fail "no apt-get here - this version only knows the apt source"
for f in "$unit" "$rollback_unit" "$rollback"; do
  [ -f "$f" ] || fail "$f missing - run the script from the repository's deploy/ folder"
done
"$bin" version >/dev/null 2>&1 || fail "$bin does not run here (wrong architecture?)"
"$bin" check-config --config "$cfg" || fail "$cfg has issues (listed above) - nothing installed"
"$bin" hub-check --config "$cfg" || fail "the hub is not reachable from here (see above) - nothing installed"
# The policy path of an instance is the one deploy/systemd/vordr-agent@.service starts it with.
mapfile -t instances < <(systemctl list-units --state=running --plain --no-legend 'vordr-agent@*.service' | awk '{print $1}')
for u in "${instances[@]}"; do
  name=${u#vordr-agent@}
  name=${name%.service}
  out=$("$bin" check-config --config "/etc/vordr/targets/$name.yaml" 2>&1) || {
    echo "$out" >&2
    fail "proxy instance $name: /etc/vordr/targets/$name.yaml has issues with the new binary (above) - nothing installed"
  }
done
if [ -f /etc/vordr/agent.yaml ] && ! cmp -s "$cfg" /etc/vordr/agent.yaml; then
  [ -n "$reason" ] || fail "/etc/vordr/agent.yaml exists and differs from $cfg - compare them and decide yourself, \
or replace it with --replace-config \"<reason>\"; nothing installed"
  diff -u /etc/vordr/agent.yaml "$cfg" || true
  backup=/etc/vordr/agent.yaml.bak-$(date +%Y%m%d-%H%M%S)
  cp -p /etc/vordr/agent.yaml "$backup"
  logger -t install-agent "agent.yaml replaced (old one in $backup): $reason"
  echo "install-agent: agent.yaml will be replaced, old one kept as $backup - reason: $reason"
fi

install -d -m 0755 /etc/vordr
install -m 0600 "$cfg" /etc/vordr/agent.yaml
install -m 0755 "$bin" /usr/local/bin/vordr-agent.new
mv -f /usr/local/bin/vordr-agent.new /usr/local/bin/vordr-agent
# A manual install is a fresh start: no self-update is pending any more.
rm -f /var/lib/vordr/update.json /usr/local/bin/vordr-agent.old
install -d -m 0755 /usr/local/lib/vordr
install -m 0755 "$rollback" /usr/local/lib/vordr/rollback.sh
install -m 0644 "$rollback_unit" /etc/systemd/system/vordr-agent-rollback.service
install -m 0644 "$unit" /etc/systemd/system/vordr-agent.service
systemctl daemon-reload
systemctl enable --quiet vordr-agent.service
systemctl restart vordr-agent.service
sleep 5
if ! systemctl is-active --quiet vordr-agent.service; then
  journalctl -u vordr-agent.service -n 30 --no-pager || true
  fail "the service did not stay up (journal above)"
fi
# The running instances still run the replaced binary; only now that the new one stays up do they follow it.
if [ ${#instances[@]} -gt 0 ]; then
  systemctl try-restart "${instances[@]}"
  sleep 5
  for u in "${instances[@]}"; do
    if ! systemctl is-active --quiet "$u"; then
      journalctl -u "$u" -n 30 --no-pager || true
      fail "proxy instance $u did not come back with the new binary (journal above)"
    fi
  done
  echo "install-agent: proxy instances restarted onto the new binary: ${instances[*]}"
fi
ident=$(/usr/local/bin/vordr-agent identity --config /etc/vordr/agent.yaml)
node=$(awk '$1 == "node" {print $2}' <<<"$ident")
fp=$(awk '$1 == "fingerprint" {print $2}' <<<"$ident")
echo "vordr-agent $(/usr/local/bin/vordr-agent version) runs (node $node, fingerprint $fp)."
echo "A new host waits for approval; at the hub, after comparing (an approved host keeps its approval):"
echo "  vordr-hub approve $node --fingerprint $fp"
