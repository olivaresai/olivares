#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Generated from the accepted input lock; a moved lock refuses before any network effect.
set -euo pipefail
cd /toolchain
printf '%s  %s\n' '045db52da769fcd19a91261d722c2f4542b47a5566711ee78cb1f967d0a112c1' input-lock.json | sha256sum -c -
# HTTP bootstrap does not grant trust: apt verifies the archive signature and the exact
# InRelease and decoded index bytes below must match before any package installation.
rm -f /etc/apt/sources.list /etc/apt/sources.list.d/*
printf '%s\n' 'deb [check-valid-until=no signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://snapshot.debian.org/archive/debian/20260922T000000Z/ trixie main' > /etc/apt/sources.list.d/appliance-snapshot.list
apt-get -o Acquire::Retries=0 -o Acquire::http::Timeout=30 update
releases=(/var/lib/apt/lists/*_InRelease)
indexes=(/var/lib/apt/lists/*_main_binary-amd64_Packages*)
[ "${#releases[@]}" -eq 1 ] && [ "${#indexes[@]}" -eq 1 ]
printf '%s  %s\n' '0584fba32e13e0ab8285fb16c27adea1ec03a73669c18702821094fd6ca86675' "${releases[0]}" | sha256sum -c -
/usr/lib/apt/apt-helper cat-file "${indexes[0]}" > /tmp/appliance-Packages
printf '%s  %s\n' '4f2c68d67001d595fbd343f6dbad44468953a51c4d9dec3a4803249f6e295940' /tmp/appliance-Packages | sha256sum -c -
apt-get -o Acquire::Retries=0 -o Acquire::http::Timeout=30 install -y --no-install-recommends \
    apt=3.0.3 \
    bash=5.2.37-2+b10 \
    btrfs-progs=6.14-1 \
    ca-certificates=20250419 \
    cpio=2.15+dfsg-2 \
    cryptsetup=2:2.7.5-2 \
    curl=8.14.1-2+deb13u5 \
    dosfstools=4.2-1.2 \
    dpkg=1.22.22 \
    dpkg-dev=1.22.22 \
    e2fsprogs=1.47.2-3+b12 \
    erofs-utils=1.8.6-1 \
    file=1:5.46-5 \
    gdisk=1.0.10-2 \
    gnupg=2.4.7-21+deb13u1 \
    grub-efi-amd64-bin=2.12-9+deb13u2 \
    grub-pc-bin=2.12-9+deb13u2 \
    isolinux=3:6.04~git20190206.bf6db5b4+dfsg1-3.1 \
    isomd5sum=1:1.2.3-5+b8 \
    kpartx=0.11.1-2 \
    lsof=4.99.4+dfsg-2 \
    lvm2=2.03.31-2 \
    mdadm=4.4-11 \
    mtools=4.0.48-1 \
    openssl=3.5.7-1~deb13u2 \
    python3=3.13.5-1 \
    python3-packaging=25.0-1 \
    python3-pip=25.1.1+dfsg-1 \
    python3-venv=3.13.5-1 \
    qemu-utils=1:10.0.13+ds-0+deb13u1 \
    rsync=3.4.1+ds1-5+deb13u4 \
    screen=4.9.1-3 \
    sed=4.9-2+deb13u1 \
    squashfs-tools=1:4.6.1-1 \
    syslinux=3:6.04~git20190206.bf6db5b4+dfsg1-3.1 \
    tar=1.35+dfsg-3.1 \
    util-linux=2.41.5-0+deb13u1 \
    xfsprogs=6.13.0-2+deb13u1 \
    xorriso=1.5.6-1.2+b1
rm /tmp/appliance-Packages
# Retain only installed inventory, not package archives; no second repository is enabled.
rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb
