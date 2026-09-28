<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Fedora 44 appliance image build toolchain

This is the builder of the shipping `fedora44-server-amd64` profile, the successor of the Debian 13
builder one directory up. It is derived from those files by counted changes; the Debian builder
stays for the non-shipping `debian13-server-amd64` profile.

- `Containerfile` starts from `registry.fedoraproject.org/fedora:44`, pinned by its linux/amd64
  manifest digest. `input-lock.json` records that digest and the digest of the image index.
- `input-lock.json` pins the Fedora 44 Everything releases and updates trees by the sha256 of their
  `repomd.xml`, and the Fedora 44 key by sha256 and fingerprint. Every RPM the builder installs
  (`distribution.system_packages`) and every root the Fedora profile names (`image_packages`) is
  locked by NEVRA and sha256 from that metadata.
- `bootstrap.sh` refuses a moved lock or key, caches the two trees' metadata with dnf5, checks their
  `repomd.xml` in the cache against the pins, and installs exactly the locked NEVRAs from that
  cache.
- `install.py` builds KIWI 11.0.4 from its verified source (the same sdist and sha256 as the Debian
  lock) in a venv over Fedora's own Python packages. Fedora 44's `python3-kiwi` is 11.0.2 and is
  never installed.

Nothing here has been built. The hosted preflight (`appliance/test/runner`) still builds and
qualifies the Debian builder: its collector names `toolchain/Containerfile` and that directory as
the build context. Qualifying this builder is the next round's work, and so is reading its
`rpm-inventory.txt`. The updates tree keeps only current builds, so its pin holds until the next
updates push. The pin is then re-pinned, never loosened.
