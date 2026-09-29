#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Test-only setup inside a disposable Fedora guest, never the host or product package.
set -euo pipefail
[[ $(id -u) == 0 && -e /run/olivares-network-guest-authorized ]] || exit 2
out=/var/lib/olivares-network-guest
mkdir -p "$out"
case ${1:-} in
 prepare)
  getenforce > "$out/selinux-before.txt"
  [[ $(cat "$out/selinux-before.txt") == Enforcing ]] || exit 2
  systemctl list-unit-files > "$out/generic-unit-files.txt"
  ip -j addr > "$out/generic-addresses.json"
  ip -j route show table all > "$out/generic-routes.json"
  find /etc/network /etc/sysconfig/network-scripts /etc/systemd/network /run/systemd/network /usr/lib/systemd/network /etc/netplan -type f -print > "$out/generic-stack-files.txt" 2>/dev/null || true
  dnf -y install /var/tmp/network-payload/*.rpm > "$out/package-install.log" 2>&1
  install -m 0755 /var/tmp/network-payload/network-guest-driver /usr/local/libexec/network-guest-driver
  mkdir -p "$out/removed-stack"
  # Qualification has not started. Retain every displaced conflicting owner.
  for dir in /etc/network/interfaces.d /etc/sysconfig/network-scripts /etc/systemd/network /run/systemd/network /usr/lib/systemd/network /etc/netplan; do
   [[ -d $dir ]] || continue
   while IFS= read -r -d '' file; do
    dest="$out/removed-stack$file";mkdir -p "$(dirname "$dest")";mv "$file" "$dest"
   done < <(find "$dir" -maxdepth 1 -type f \( -name '*.network' -o -name 'ifcfg-*' -o -name '*.yaml' -o -name '*.yml' \) -print0)
  done
  cp -a /var/lib/cloud/seed/nocloud "$out/setup-seed" 2>/dev/null || cp -a /var/lib/cloud/seed/nocloud-net "$out/setup-seed"
  cloud-init clean --logs
  ;;
 qualify)
  [[ $(getenforce) == Enforcing ]] || exit 2
  cloud-init status --wait > "$out/cloud-init-status.txt" 2>&1 || { [[ $? == 2 ]] || exit 2; }
  rpm -q NetworkManager systemd dbus-broker polkit > "$out/installed-versions.txt"
  # First boot may stop at product setup. Only the completed host stages are credited.
  appliance-firstboot apply > "$out/firstboot-apply.txt" 2>&1 || true
  python3 - <<'PY'
import json
p=json.load(open('/var/lib/olivares-appliance/state.json'))
stages=[s['stage'] for s in p['completed']]
assert 'verify-host-settings' in stages and 'hand-over-host-settings' in stages
assert stages.index('verify-host-settings')<stages.index('hand-over-host-settings')
assert p['host_settings_owner']=='appliance'
PY
  /usr/local/libexec/network-guest-driver verify-firstboot > "$out/host-verified.txt"
  mkdir -p /etc/network/interfaces.d
  printf 'iface nic1 inet static\n' > /etc/network/interfaces.d/network-core-negative
  if /usr/local/libexec/network-guest-driver verify-firstboot > "$out/second-owner-negative.txt" 2>&1; then exit 1; fi
  grep -q second_network_owner "$out/second-owner-negative.txt"
  rm /etc/network/interfaces.d/network-core-negative
  # This ends the explicit negative; no cleanup occurs within a positive case.
  systemctl stop olivares-net-guard.service
  systemctl mask olivares-net-guard.service
  /usr/libexec/olivares/olivares-net-guard initialize-runtime
  cat > /etc/systemd/system/olivares-network-root-fixture.service <<'UNIT'
[Unit]
Description=Test-only root restoration library fixture
After=NetworkManager.service
[Service]
ExecStart=/usr/local/libexec/network-guest-driver root-fixture
User=root
Group=olivares-net-guard
RuntimeDirectory=olivares-network-root-fixture
RuntimeDirectoryMode=0750
[Install]
WantedBy=multi-user.target
UNIT
  cat > /etc/systemd/system/olivares-network-core-fixture.service <<'UNIT'
[Unit]
Description=Test-only real-NM engine fixture as the actual guard account
Requires=olivares-network-runtime.service olivares-network-root-fixture.service
After=NetworkManager.service olivares-network-runtime.service olivares-network-root-fixture.service
[Service]
ExecStart=/usr/local/libexec/network-guest-driver guard
# Labelled reachability targets: the user-mode resolver of nic1's networks (run-hosted.sh).
Environment=NETWORK_CORE_REACH_TARGETS=10.0.3.3,fd00:33::3
User=olivares-net-guard
Group=olivares-net-guard
RuntimeDirectory=olivares-network-core
RuntimeDirectoryMode=0700
StateDirectory=olivares-net-guard
StateDirectoryMode=0700
Restart=on-failure
RestartSec=2
[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now olivares-network-root-fixture.service olivares-network-core-fixture.service
  ;;
 ipv6-fixture)
  [[ $(getenforce) == Enforcing ]] || exit 2
  dnf -y install dnsmasq tcpdump > "$out/ipv6-fixture-packages.log" 2>&1
  ip netns add network-core-router
  ip link add dhcp6c type veth peer name dhcp6s
  ip link set dhcp6s netns network-core-router
  ip netns exec network-core-router ip link set lo up
  ip netns exec network-core-router ip link set dhcp6s up
  ip netns exec network-core-router ip -6 addr add fd00:66::1/64 dev dhcp6s
  ip link set dhcp6c up
  systemd-run --unit=network-core-dhcp6 --property=Type=exec /usr/sbin/ip netns exec network-core-router /usr/sbin/dnsmasq --keep-in-foreground --conf-file=/dev/null --interface=dhcp6s --bind-interfaces --port=0 --enable-ra --dhcp-range=fd00:66::100,fd00:66::200,64,120s --dhcp-leasefile="$out/dhcp6.leases" --log-dhcp --log-facility="$out/dhcp6-server.log"
  systemd-run --unit=network-core-dhcp6-capture --property=Type=exec /usr/sbin/tcpdump -U -n -i dhcp6c -w "$out/dhcp6.pcap" 'udp port 546 or udp port 547'
  nmcli connection add type ethernet ifname dhcp6c con-name network-core-dhcp6 ipv4.method disabled ipv6.method dhcp ipv6.never-default yes ipv6.ignore-auto-dns yes
  nmcli connection up network-core-dhcp6
  python3 - <<'MEASURE'
import json,pathlib,subprocess,time
out=pathlib.Path('/var/lib/olivares-network-guest')
def lease():
 try:
  rows=[r for r in (out/'dhcp6.leases').read_text().splitlines() if r and not r.startswith('duid')]
  return rows[0] if rows else ''
 except FileNotFoundError:return ''
def address():
 data=json.loads(subprocess.check_output(['ip','-j','-6','addr','show','dev','dhcp6c']))
 return [a['local'] for d in data for a in d.get('addr_info',[]) if a.get('scope')=='global']
limit=time.monotonic()+60
while time.monotonic()<limit and not (lease() and address()):time.sleep(1)
first=lease();initial=address();assert first and initial
(out/'dhcp6-first.json').write_text(json.dumps({'first_lease':first,'first_at':int(time.time()),'initial_addresses':initial}))
MEASURE
  # The capture keeps running across the nic1 change; ipv6-renewal stops it.
  ;;
 ipv6-renewal)
  [[ $(getenforce) == Enforcing ]] || exit 2
  python3 - <<'MEASURE'
import json,pathlib,subprocess,time
out=pathlib.Path('/var/lib/olivares-network-guest')
def lease():
 try:
  rows=[r for r in (out/'dhcp6.leases').read_text().splitlines() if r and not r.startswith('duid')]
  return rows[0] if rows else ''
 except FileNotFoundError:return ''
first=json.loads((out/'dhcp6-first.json').read_text())
# A renewal counts only when it arrives after this step starts, that is after the revert.
current=lease();limit=time.monotonic()+150
while time.monotonic()<limit and lease() in ('',current):time.sleep(2)
renewed=lease();final=[a['local'] for d in json.loads(subprocess.check_output(['ip','-j','-6','addr','show','dev','dhcp6c'])) for a in d.get('addr_info',[]) if a.get('scope')=='global']
assert renewed and renewed!=current and final
(out/'dhcp6-measured.json').write_text(json.dumps({'first_lease':first['first_lease'],'first_at':first['first_at'],'renewed_lease':renewed,'renewed_at':int(time.time()),'initial_addresses':first['initial_addresses'],'renewed_addresses':final,'acquired':True}))
MEASURE
  systemctl stop network-core-dhcp6-capture.service
  ;;
 collect)
  getenforce > "$out/selinux-after.txt"
  ausearch -m AVC,USER_AVC -ts boot > "$out/avcs.txt" 2>&1 || [[ $? == 1 ]]
  journalctl -b --no-pager -u NetworkManager -u olivares-network-core-fixture -u olivares-network-root-fixture > "$out/services.log"
  ip -j addr > "$out/final-addresses.json"
  ip -j route show table all > "$out/final-routes.json"
  ;;
 *) exit 2 ;;
esac
