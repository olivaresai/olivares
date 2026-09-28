#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# The Fedora 44 successor of ../bootstrap.sh, generated from the accepted fedora44/input-lock.json; a moved lock
# refuses before any network effect.
set -euo pipefail
cd /toolchain
printf '%s  %s\n' 'a374d48707b18af5018dc5be9829d881a60b7279b726679dd733c726c3adaed7' input-lock.json | sha256sum -c -
# HTTPS does not grant trust: every package is checked against the Fedora 44 key, whose file the base image's
# fedora-gpg-keys installed and whose sha256 is checked first. Fedora does not sign repomd.xml, so dnf5 caches the
# metadata of the two pinned trees, their repomd.xml sha256 is checked in the cache, and only then does dnf5 install,
# from that cache (-C), exactly the locked NEVRAs. The base image's own repositories are removed.
printf '%s  %s\n' '93642aec521a1e5e96dd715f7ae0ec0850ebc9de09a94ce03cae5263f26cc18a' /etc/pki/rpm-gpg/RPM-GPG-KEY-fedora-44-primary | sha256sum -c -
rm -f /etc/yum.repos.d/*.repo
cat > /etc/yum.repos.d/appliance-pinned.repo <<'REPO'
[fedora-44-releases]
name=fedora-44-releases
baseurl=https://dl.fedoraproject.org/pub/fedora/linux/releases/44/Everything/x86_64/os/
gpgcheck=1
repo_gpgcheck=0
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-fedora-44-primary

[fedora-44-updates]
name=fedora-44-updates
baseurl=https://dl.fedoraproject.org/pub/fedora/linux/updates/44/Everything/x86_64/
gpgcheck=1
repo_gpgcheck=0
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-fedora-44-primary

REPO
dnf5 -y makecache --refresh
while read -r repo pinned; do
  cached=(/var/cache/libdnf5/"$repo"-*/repodata/repomd.xml)
  [ "${#cached[@]}" -eq 1 ]
  printf '%s  %s\n' "$pinned" "${cached[0]}" | sha256sum -c -
done <<'PINS'
fedora-44-releases da3845427d188097f6fd71b417a039bdfb8efefc4f38ca44b5cbb94f95a18991
fedora-44-updates 204b9f69d50089542161aaab293a8ddce072ea7130d1c0dbb76737d6c15ee7b8
PINS
dnf5 -y -C install --setopt=install_weak_deps=False \
    bash-5.3.9-3.fc44.x86_64 \
    btrfs-progs-7.1-1.fc44.x86_64 \
    bzip2-1.0.8-23.fc44.x86_64 \
    ca-certificates-2026.2.90_v9.0.317-1.fc44.noarch \
    cpio-2.15-9.fc44.x86_64 \
    createrepo_c-1.2.1-5.fc44.x86_64 \
    cryptsetup-2.8.8-1.fc44.x86_64 \
    curl-8.18.0-10.fc44.x86_64 \
    dnf5-5.4.5.0-1.fc44.x86_64 \
    dosfstools-4.2-18.fc44.x86_64 \
    e2fsprogs-1.47.3-4.fc44.x86_64 \
    erofs-utils-1.9.4-1.fc44.x86_64 \
    fedora-gpg-keys-44-2.noarch \
    file-5.46-10.fc44.x86_64 \
    gdisk-1.0.10-5.fc44.x86_64 \
    gnupg2-2.4.9-16.fc44.x86_64 \
    grub2-efi-x64-modules-1:2.12-64.fc44.noarch \
    grub2-pc-modules-1:2.12-64.fc44.noarch \
    grub2-tools-1:2.12-64.fc44.x86_64 \
    gzip-1.14-2.fc44.x86_64 \
    isomd5sum-1:1.2.5-6.fc44.x86_64 \
    kpartx-0.13.1-1.fc44.x86_64 \
    lsof-4.98.0-9.fc44.x86_64 \
    lvm2-2.03.38-2.fc44.x86_64 \
    make-1:4.4.1-12.fc44.x86_64 \
    mdadm-4.3-11.fc44.x86_64 \
    mtools-4.0.49-3.fc44.x86_64 \
    openssl-1:3.5.8-1.fc44.x86_64 \
    python3-3.14.7-1.fc44.x86_64 \
    python3-lxml-6.1.1-1.fc44.x86_64 \
    python3-packaging-25.0-8.fc44.noarch \
    python3-pip-26.0.1-3.fc44.noarch \
    python3-poetry-core-2.3.0-1.fc44.noarch \
    python3-pyyaml-6.0.3-3.fc44.x86_64 \
    python3-requests-2.33.1-1.fc44.noarch \
    python3-simplejson-3.19.3-7.fc44.x86_64 \
    python3-typer-0.25.1-1.fc44.noarch \
    qemu-img-2:10.2.2-1.fc44.x86_64 \
    rpm-6.0.2-1.fc44.x86_64 \
    rpm-build-6.0.2-1.fc44.x86_64 \
    rpm-sign-6.0.2-1.fc44.x86_64 \
    rsync-3.5.0-2.fc44.x86_64 \
    screen-5.0.1-6.fc44.x86_64 \
    sed-4.9-7.fc44.x86_64 \
    selinux-policy-44.10-1.fc44.noarch \
    selinux-policy-devel-44.10-1.fc44.noarch \
    squashfs-tools-4.6.1-8.fc44.x86_64 \
    syslinux-6.04-0.37.fc44.x86_64 \
    syslinux-nonlinux-6.04-0.37.fc44.noarch \
    tar-2:1.35-9.fc44.x86_64 \
    util-linux-2.41.5-1.fc44.x86_64 \
    xfsprogs-7.1.1-1.fc44.x86_64 \
    xorriso-1.5.8-2.fc44.x86_64
# Retain only the installed inventory, not the package cache; no repository stays configured.
dnf5 clean all
rm -f /etc/yum.repos.d/appliance-pinned.repo
