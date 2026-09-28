#!/bin/bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# config.sh — what KIWI NG runs inside the image root once the packages are installed.
#
# It is deliberately short, and what is NOT here is the point. First boot, the console label,
# the firewall, the product's configuration and every instance identity belong to the
# appliance layer, which is installed as a package and enables itself (design 5.2). A recipe
# script that did any of that would be untestable without building 2 GB, and it would never
# reach an appliance that is already installed.
#
# What belongs here is the image's own state: a machine that has never booted. The last thing
# it does is ask the layer's own gate whether this root is still a template, and a root that
# carries an identity fails the build here rather than being cloned into every instance.
set -euxo pipefail

# KIWI NG's build environment, if this runs under KIWI (both files are KIWI's own).
test -f /.kconfig && . /.kconfig
test -f /.profile && . /.profile

# --- the base -------------------------------------------------------------------------------
# One description, one profile per base (config.xml). KIWI names the profiles this build takes,
# comma-separated, in kiwi_profiles (system/profile.py :359-363); a root of neither base stops here.
case ",${kiwi_profiles:-}," in
  *,fedora44-server-amd64,*) base=fedora44 ;;
  *,debian13-server-amd64,*) base=debian13 ;;
  *) printf 'config.sh: no base profile in kiwi_profiles=%s\n' "${kiwi_profiles:-}" >&2; exit 1 ;;
esac

# --- SELinux (Fedora) -------------------------------------------------------------------------
# Enforcing with the targeted policy, and no other path (Root c1efc2ee §2). selinux-policy writes
# /etc/selinux/config from a scriptlet, so the mode the appliance relies on is written here and
# checked. KIWI labels the root after this script only when the policy's file_contexts exists and
# setfiles is installed, and otherwise just warns (system/setup.py :633-645): such a root fails here.
if [ "$base" = fedora44 ]; then
  cat > /etc/selinux/config <<'SELINUX'
# Olivares appliance: SELinux enforcing with the targeted policy (the recipe's config.sh).
SELINUX=enforcing
SELINUXTYPE=targeted
SELINUX
  grep -qx 'SELINUX=enforcing' /etc/selinux/config
  test -s /etc/selinux/targeted/contexts/files/file_contexts
  command -v setfiles
fi

# --- the update interface (Fedora) ----------------------------------------------------------
# F5 r3 §3 and §7-§8: one automatic route, so dnf5-automatic stays disabled (the overlay's preset
# says so and no enablement link may exist); our key and repository file are in place; and
# installonly_limit comes from the overlay's drop-in, which /etc/dnf/dnf.conf, loaded last, must not
# override (dnf5.conf(5)).
if [ "$base" = fedora44 ]; then
  for unit in dnf5-automatic.timer dnf-automatic.timer; do
    if [ -e "/etc/systemd/system/timers.target.wants/$unit" ] || [ -L "/etc/systemd/system/timers.target.wants/$unit" ]; then
      printf 'config.sh: %s is enabled; the product timer is the one automatic route\n' "$unit" >&2
      exit 1
    fi
  done
  grep -qx 'disable dnf5-automatic.timer' /etc/systemd/system-preset/10-olivares-appliance.preset
  grep -qx 'installonly_limit=3' /etc/dnf/libdnf5.conf.d/20-olivares-appliance.conf
  if [ -f /etc/dnf/dnf.conf ] && grep -q '^[[:space:]]*installonly_limit' /etc/dnf/dnf.conf; then
    printf 'config.sh: /etc/dnf/dnf.conf sets installonly_limit and would override the recipe\n' >&2
    exit 1
  fi
  test -s /etc/pki/rpm-gpg/RPM-GPG-KEY-olivares-package-repository
  grep -qx 'gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-olivares-package-repository' /etc/yum.repos.d/olivares.repo
fi

# --- the per-build key (Fedora) ---------------------------------------------------------------
# The key build.sh made for the in-build dracut RPM signs nothing else, and its secret half is gone, but dnf5 may have
# imported its public half into this root's rpm keyring when it installed that RPM. It leaves the image here, by the
# fingerprint build.sh recorded in the overlay, and a root whose keyring still lists it fails. rpmkeys(8) at rpm 6.0.2:
# --list prints "<fingerprint> <name> <userid> public key", and --erase takes the fingerprint.
if [ "$base" = fedora44 ]; then
  record=/var/lib/olivares-appliance-build/per-build-key.fingerprint
  build_key=""
  if [ -f "$record" ]; then build_key=$(cat "$record"); fi
  if ! [[ $build_key =~ ^[0-9A-F]{40}$ ]]; then
    printf 'config.sh: %s does not name the per-build key (%s)\n' "$record" "$build_key" >&2
    exit 1
  fi
  listed_build_key() { rpmkeys --list | awk -v key="${build_key,,}" 'tolower($1) == key { print $1 }'; }
  for key in $(listed_build_key); do
    rpmkeys --erase "$key"
  done
  if [ -n "$(listed_build_key)" ]; then
    printf 'config.sh: the per-build key %s is still in the rpm keyring\n' "$build_key" >&2
    exit 1
  fi
  rm -rf /var/lib/olivares-appliance-build
fi

# --- the enabled repositories (Fedora) --------------------------------------------------------
# fedora-repos 44-2 enables fedora-cisco-openh264 by default: Fedora-signed builds that Cisco serves, which no package of
# this image comes from and the image's trust chain does not name. Its section is turned off in the package's own file,
# a %config(noreplace) file that an update keeps. Then the enabled repositories must be exactly Fedora's two and ours
# (dnf5 enables a section that says nothing), with no dnf5 repository override, which could change enabled after the
# .repo files (dnf5.conf(5) at 5.4.5.0, "Drop-in repo directories").
if [ "$base" = fedora44 ]; then
  openh264=/etc/yum.repos.d/fedora-cisco-openh264.repo
  if [ -f "$openh264" ]; then
    sed -i '/^\[fedora-cisco-openh264\]$/,/^\[/ s/^[[:space:]]*enabled[[:space:]]*=.*$/enabled=0/' "$openh264"
  fi
  enabled=$(awk '
    FNR == 1 { section = "" }
    /^[[:space:]]*\[.*\][[:space:]]*$/ {
      section = $0; gsub(/^[[:space:]]*\[|\][[:space:]]*$/, "", section); on[section] = 1; next }
    section != "" && /^[[:space:]]*enabled[[:space:]]*=/ {
      value = $0; sub(/^[^=]*=[[:space:]]*/, "", value); sub(/[[:space:]]+$/, "", value)
      on[section] = (tolower(value) ~ /^(1|true|yes|on)$/) }
    END { for (s in on) if (on[s]) print s }' /etc/yum.repos.d/*.repo | sort | tr '\n' ' ')
  if [ "$enabled" != "fedora olivares updates " ]; then
    printf 'config.sh: the enabled repositories are %s; the image enables fedora, updates and olivares only\n' \
      "$enabled" >&2
    exit 1
  fi
  for directory in /etc/dnf/repos.override.d /usr/share/dnf5/repos.override.d; do
    if [ -d "$directory" ] && [ -n "$(ls -A "$directory")" ]; then
      printf 'config.sh: %s holds a repository override: %s\n' "$directory" "$(ls -A "$directory" | tr '\n' ' ')" >&2
      exit 1
    fi
  done
fi

# --- the console -------------------------------------------------------------------------
# The kernel command line of the image type already carries console=ttyS0. This makes the
# boot menu itself answer on the same serial port, so an appliance with no screen can be
# recovered and the boot battery can read what the bootloader did. Debian's grub-mkconfig reads
# /etc/default/grub.d; Fedora's grub2-mkconfig reads /etc/default/grub only (grub2-tools
# 2.12-64.fc44), which KIWI writes from the Fedora type's bootloader attributes, the same settings.
if [ "$base" = debian13 ]; then
  mkdir -p /etc/default/grub.d
  cat > /etc/default/grub.d/99-olivares-appliance.cfg <<'GRUB'
GRUB_TERMINAL="console serial"
GRUB_SERIAL_COMMAND="serial --unit=0 --speed=115200 --word=8 --parity=no --stop=1"
GRUB_TIMEOUT=3
GRUB_DISABLE_OS_PROBER=true
GRUB
fi

# --- the host settings' owner --------------------------------------------------------------
# cloud-init owns hostname, network, time and the authorized keys; the appliance layer waits
# for it and verifies them, and never applies them itself (design 11.2). The datasources are
# the carriers the layer documents: a NoCloud seed, the OVF environment of a hypervisor, and
# None so a machine with no seed still boots to the console label instead of stalling.
mkdir -p /etc/cloud/cloud.cfg.d
cat > /etc/cloud/cloud.cfg.d/90-olivares-appliance.cfg <<'CLOUD'
# SPDX-License-Identifier: AGPL-3.0-only
datasource_list: [ NoCloud, ConfigDrive, OVF, VMware, None ]
CLOUD

# --- a machine that has never booted --------------------------------------------------------
# systemd makes the machine ID on each instance's first boot from an EMPTY /etc/machine-id, and
# does not count that boot as a first boot (machine-id(5)), so no ConditionFirstBoot= unit runs.
# None should: Debian's systemd-firstboot.service would prompt on the console for a root
# password. The SSH host keys are made on that boot, before ssh.service: by cloud-init when it
# has a datasource (it orders itself before sshd-keygen.service), else by Debian's
# sshd-keygen.service, which the drop-in below runs whenever no host key exists instead of on a
# first boot only. Fedora's sshd-keygen@.service already runs whenever its key is missing
# (ConditionFileNotEmpty=|!/etc/ssh/ssh_host_%i_key, openssh-server 10.2p1-14.fc44), and sshd.service
# wants it. The layer records the keys as this instance's.
: > /etc/machine-id
rm -f /etc/ssh/ssh_host_*
if [ "$base" = debian13 ]; then
  mkdir -p /etc/systemd/system/sshd-keygen.service.d
  cat > /etc/systemd/system/sshd-keygen.service.d/olivares-appliance.conf <<'UNIT'
# Olivares appliance: make the SSH host keys whenever none exists, not on a first boot only.
# An empty condition resets every condition of the unit (systemd.unit(5)); the two Debian
# conditions that still apply are restated.
[Unit]
ConditionFirstBoot=
ConditionPathIsReadWrite=/etc/ssh
ConditionPathIsSymbolicLink=!/etc/ssh
ConditionPathExistsGlob=!/etc/ssh/ssh_host_*_key
UNIT
fi
rm -f /var/lib/systemd/random-seed
rm -rf /var/lib/cloud/instance /var/lib/cloud/instances /var/lib/cloud/data
rm -f /etc/resolv.conf.bak /etc/hostname.bak

# The build's package index and downloads are not part of the appliance. On Fedora, KIWI keeps
# dnf5's cache in the builder, outside the root; a cache the image's own tools made is removed.
case $base in
  debian13)
    apt-get clean || true
    rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb ;;
  fedora44)
    rm -rf /var/cache/libdnf5 /var/cache/dnf ;;
esac

# --- the SELinux build checks (Fedora) ------------------------------------------------------------
# After the package set, each failing the build. olivares-selinux's %post installs the appliance's policy module at
# priority 200 and records its four ports in the policy store; with no policy loaded in the build, semanage writes the
# store without a reload, and semodule and semanage read it here. The root is labeled at build (KIWI's setfiles, after
# this script; images.sh checks the module's paths then), never by an autorelabel on boot, because Fedora's relabel
# service lowers enforcement while it runs.
if [ "$base" = fedora44 ]; then
  if ! semodule -lfull | grep -Eq '^200 olivares[[:space:]]'; then
    printf 'config.sh: the olivares policy module is not installed at priority 200 (semodule -lfull)\n' >&2
    exit 1
  fi
  ports=$(semanage port -l -C)
  for record in 'olivares_console_port_t tcp 8443' 'olivares_grpc_port_t tcp 8444' 'olivares_portal_port_t tcp 9443' \
      'olivares_model_runtime_port_t tcp 11434'; do
    read -r type proto port <<<"$record"
    if ! grep -Eq "^${type}[[:space:]]+${proto}[[:space:]]+${port}[[:space:]]*\$" <<<"$ports"; then
      printf 'config.sh: no port record %s in the policy store (semanage port -l -C)\n' "$record" >&2
      exit 1
    fi
  done
  if [ -e /.autorelabel ]; then
    printf 'config.sh: /.autorelabel asks for a relabel on first boot, which runs with enforcement lowered\n' >&2
    exit 1
  fi
fi

# --- the gate --------------------------------------------------------------------------------
# The layer's own template check, run over the finished root. It exits 1 naming every instance
# identity it finds - SSH host keys, the machine ID, the product's TLS key, the setup token,
# the audit, catalog and policy signing keys, the store and a first-boot record - so an image
# that would clone an identity into every appliance fails HERE, not in the field.
/usr/bin/appliance-firstboot check-template /
