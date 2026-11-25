#!/usr/bin/env bash
# Optional Linux kernel tuning for very high connection counts and load tests.
#
# Not required to run the service. It helps when one machine must handle tens
# of thousands of concurrent sockets. Changes apply at runtime only and reset
# on reboot; copy them into /etc/sysctl.d/ to make them permanent.
#
# Deliberately NOT included (common in old tuning guides, but harmful):
#   net.ipv4.tcp_tw_recycle    removed in Linux 4.12; broke clients behind NAT
#   net.ipv4.tcp_syncookies=0  turns off SYN-flood protection
#
# Usage: sudo scripts/tune-host.sh --apply   (without --apply: dry run)
set -euo pipefail

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "This script only applies to Linux hosts." >&2
  exit 1
fi

DRY_RUN=true
[[ "${1:-}" == "--apply" ]] && DRY_RUN=false

SETTINGS=(
  "net.core.somaxconn=65535"                # larger accept() backlog
  "net.ipv4.tcp_max_syn_backlog=65535"      # more half-open connections queued
  "net.core.netdev_max_backlog=250000"      # NIC -> kernel queue during bursts
  "net.ipv4.ip_local_port_range=1024 65535" # more ephemeral ports for clients
  "net.ipv4.tcp_tw_reuse=1"                 # reuse TIME_WAIT sockets for outbound
  "net.ipv4.tcp_fin_timeout=15"             # free closed sockets sooner
  "net.ipv4.tcp_fastopen=3"                 # TCP Fast Open, client and server
  "net.ipv4.tcp_keepalive_time=60"          # detect dead peers faster
  "net.ipv4.tcp_keepalive_intvl=10"
  "net.ipv4.tcp_keepalive_probes=5"
  "net.ipv4.tcp_rmem=4096 87380 6291456"    # socket receive buffer min/default/max
  "net.ipv4.tcp_wmem=4096 87380 6291456"    # socket send buffer min/default/max
  "fs.file-max=2097152"                     # system-wide open file limit
)

for setting in "${SETTINGS[@]}"; do
  if $DRY_RUN; then
    echo "[dry-run] sysctl -w \"$setting\""
  else
    sysctl -w "$setting"
  fi
done

if $DRY_RUN; then
  echo
  echo "Dry run only. Re-run with --apply (as root) to change kernel settings."
else
  echo "Kernel settings applied."
fi
echo "Also raise the per-process file limit: 'ulimit -n 1048576', or use"
echo "'make docker-up' which passes --ulimit nofile=1048576:1048576."
