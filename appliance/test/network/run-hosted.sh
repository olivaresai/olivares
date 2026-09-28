#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Hosted disposable guest only. This file is prepared by the author, never run locally.
set -euo pipefail
payload=${1:?payload directory with two RPMs and network-guest-driver}
out=${2:?new evidence directory}
head=${3:?full public candidate commit}
[[ $head =~ ^[0-9a-f]{40}$ && ! -e $out ]] || exit 2
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$out/image" "$out/seed" "$out/guest" "$out/keyhome"
out=$(cd "$out" && pwd)
chmod 700 "$out/keyhome"
for tool in qemu-system-x86_64 qemu-img xorriso ssh ssh-keygen scp gpgv curl python3; do command -v "$tool" > /dev/null || exit 2; done
id > "$out/runner-id.txt"
ls -l /dev/kvm > "$out/kvm.txt" 2>&1 || true
cp "$here/image.json" "$out/image/identity.json"
python3 - "$here/image.json" "$out/image" <<'PY'
import hashlib,json,pathlib,sys,urllib.request
p=json.load(open(sys.argv[1]));out=pathlib.Path(sys.argv[2])
for key,name in [('sums_url','CHECKSUM'),('key_url','keyring.gpg')]:
 with urllib.request.urlopen(p[key],timeout=60) as r:(out/name).write_bytes(r.read(2**20))
assert hashlib.sha256((out/'CHECKSUM').read_bytes()).hexdigest()==p['sums_sha256']
assert f"SHA256 ({p['image_name']}) = {p['image_hash']}" in (out/'CHECKSUM').read_text()
PY
gpgv --homedir "$out/keyhome" --keyring "$out/image/keyring.gpg" --status-fd 1 "$out/image/CHECKSUM" > "$out/image/signature.txt" 2> "$out/image/signature.stderr"
grep -q '^\[GNUPG:\] VALIDSIG 36F612DCF27F7D1A48A835E4DBFCF71C6D9F90A6 ' "$out/image/signature.txt"
image_url=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["image_url"])' "$here/image.json")
timeout 900 curl --fail --location --retry 2 --output "$out/image/base.qcow2" "$image_url"
printf '%s  %s\n' 28680fe5b371a5a82ebf43a31926e086a168e59949d03969c5093e7071f90b7f "$out/image/base.qcow2" | sha256sum -c - > "$out/image/image-check.txt"
ssh-keygen -q -t ed25519 -N '' -f "$out/keyhome/id"
python3 - "$out" <<'PY'
import json,pathlib,sys
out=pathlib.Path(sys.argv[1]);key=(out/'keyhome/id.pub').read_text().strip()
answers={'schema_version':'appliance-answers/v1','source':'nocloud','host':{'owner':'cloud-init','hostname':'olivares.example.test','network':{'mode':'static','interfaces':[{'name':'nic1','ipv4':{'addresses':['10.0.3.10/24']}}]},'time':{'timezone':'UTC','servers':['time.example.test']},'ssh_authorized_keys':[key],'management_interfaces':['nic0']},'product':{'storage_profile':'single-node-prod','public_console_url':'https://olivares.example.test','update_channel':'stable','node_role':'control'},'portal':{'enabled':False,'listen':'local'}}
user={'fqdn':'olivares.example.test','prefer_fqdn_over_hostname':True,'timezone':'UTC','users':[{'name':'networktest','sudo':'ALL=(ALL) NOPASSWD:ALL','groups':['wheel'],'shell':'/bin/bash','ssh_authorized_keys':[key]}],'ssh_pwauth':False,'write_files':[{'path':'/etc/olivares-appliance/carriers/nocloud.json','permissions':'0600','owner':'root:root','content':json.dumps(answers)}]}
(out/'seed/user-data').write_text('#cloud-config\n'+json.dumps(user));(out/'seed/meta-data').write_text('instance-id: network-core-fixture\nlocal-hostname: olivares.example.test\n')
(out/'seed/network-config').write_text(json.dumps({'version':2,'renderer':'NetworkManager','ethernets':{'nic0':{'match':{'macaddress':'52:54:00:12:34:01'},'set-name':'nic0','dhcp4':True,'dhcp6':True},'nic1':{'match':{'macaddress':'52:54:00:12:34:02'},'set-name':'nic1','addresses':['10.0.3.10/24'],'dhcp4':False,'dhcp6':True,'accept-ra':True}}}))
PY
xorriso -as mkisofs -quiet -output "$out/seed.iso" -volid cidata -joliet -rock "$out/seed"
qemu-img create -q -f qcow2 -F qcow2 -b "$out/image/base.qcow2" "$out/guest.qcow2" 16G
accel=tcg
if [[ -r /dev/kvm && -w /dev/kvm ]]; then accel=kvm; fi
printf '%s\n' "$accel" > "$out/accelerator.txt"
port=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
qemu-system-x86_64 -accel "$accel" -m 4096 -smp 2 -display none -serial "file:$out/serial.log" -drive "file=$out/guest.qcow2,if=virtio,format=qcow2" -drive "file=$out/seed.iso,media=cdrom,readonly=on" -netdev "user,id=control,hostfwd=tcp:127.0.0.1:$port-:22" -device virtio-net-pci,netdev=control,mac=52:54:00:12:34:01 -netdev user,id=test,net=10.0.3.0/24,ipv6-net=fd00:33::/64 -device virtio-net-pci,netdev=test,mac=52:54:00:12:34:02 > "$out/qemu.stdout" 2> "$out/qemu.stderr" &
qemu_pid=$!
cleanup(){ kill "$qemu_pid" 2>/dev/null || true; wait "$qemu_pid" 2>/dev/null || true; rm -f "$out/keyhome/id"; }
trap cleanup EXIT
ssh_args=(-i "$out/keyhome/id" -p "$port" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=3 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$out/keyhome/known_hosts" networktest@127.0.0.1)
ready=false
for _ in $(seq 1 180); do if ssh "${ssh_args[@]}" true 2> "$out/ssh-wait.stderr"; then ready=true; break; fi; kill -0 "$qemu_pid" || exit 2; sleep 5; done
[[ $ready == true ]] || exit 2
python3 - "$out/ssh.json" "${ssh_args[@]}" <<'PY'
import json,sys
json.dump(['ssh']+sys.argv[2:],open(sys.argv[1],'w'))
PY
scp_args=(-i "$out/keyhome/id" -P "$port" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$out/keyhome/known_hosts")
ssh "${ssh_args[@]}" 'mkdir -p /var/tmp/network-payload'
scp "${scp_args[@]}" "$payload"/*.rpm "$payload/network-guest-driver" "$here/setup-guest.sh" networktest@127.0.0.1:/var/tmp/network-payload/
scp -r "${scp_args[@]}" "$out/seed" networktest@127.0.0.1:/var/tmp/network-payload/
ssh "${ssh_args[@]}" 'sudo mkdir -p /usr/local/libexec /var/lib/cloud/seed/nocloud; sudo cp /var/tmp/network-payload/seed/* /var/lib/cloud/seed/nocloud/; sudo touch /run/olivares-network-guest-authorized; sudo bash /var/tmp/network-payload/setup-guest.sh prepare'
printf '%s\n' "$head" > "$out/head.txt"
set +e
python3 "$here/guest-cases.py" "$out"
case_exit=$?
ssh "${ssh_args[@]}" 'sudo touch /run/olivares-network-guest-authorized; sudo bash /var/tmp/network-payload/setup-guest.sh collect; sudo tar -C /var/lib/olivares-network-guest -czf /var/tmp/network-guest-results.tar.gz .; sudo chmod 0644 /var/tmp/network-guest-results.tar.gz'
scp "${scp_args[@]}" networktest@127.0.0.1:/var/tmp/network-guest-results.tar.gz "$out/guest/"
collect_exit=$?
set -e
[[ $collect_exit == 0 ]] || exit 2
[[ $case_exit == 0 ]] || exit "$case_exit"
python3 "$here/oracle.py" "$out/cases.json"
# The ephemeral SSH private key is kept out of uploaded evidence.
rm -f "$out/keyhome/id"
