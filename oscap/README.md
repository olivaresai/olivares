<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# OpenSCAP / DISA STIG verification harness (SCP-09)

This directory lets a buyer **self-verify** the STIG-hardened image
(`Dockerfile.stig`) against the **authoritative upstream DISA STIG SCAP content**.
It ships a runner and a tailoring file — it does **not** ship, embed, or fabricate a
passing result. The report you read is one **you** generate.

Full context, build instructions and the honesty ledger live in
[`../docs/SCP-09-FIPS-STIG.md`](../docs/SCP-09-FIPS-STIG.md).

## What's here

| File | Purpose |
|---|---|
| `scan.sh` | Runs `oscap xccdf eval` against an image rootfs (`--image`) or the host (`--host`); writes `results.xml`, ARF, and an HTML `report`. |
| `tailoring.xml` | XCCDF 1.2 tailoring that **extends** the upstream DISA STIG profile and deselects only container-inapplicable rules (graphical login, bootloader, USB). No rule is redefined or weakened. |
| `README.md` | This file. |

## The content is upstream, not ours

The rules come from **ComplianceAsCode / `scap-security-guide`** — the public source
of DISA STIG profiles — which ships the SCAP datastream on the scanning host:

- RHEL/UBI 9 base: `/usr/share/xml/scap/ssg/content/ssg-rhel9-ds.xml`
- Default profile id: `xccdf_org.ssgproject.content_profile_stig`
- Project: <https://github.com/ComplianceAsCode/content>
- OpenSCAP scanner: <https://www.open-scap.org/>

Always confirm the profile id on *your* datastream (versions move):

```sh
oscap info /usr/share/xml/scap/ssg/content/ssg-rhel9-ds.xml | grep -i stig
```

## Prerequisites (on the SCANNING host, not in the image)

```sh
# RHEL / Fedora / UBI host:
dnf install -y openscap-scanner openscap-utils scap-security-guide
# (--image mode also needs podman or docker)
```

The product image stays lean: the scanner is operator-side tooling.

## Run it

```sh
# Scan a built image's rootfs (offline):
oscap/scan.sh --image olivares:stig

# Scan with the container tailoring:
oscap/scan.sh --image olivares:stig --tailoring oscap/tailoring.xml

# Scan the host OS directly:
oscap/scan.sh --host

# Pin a specific datastream / profile:
oscap/scan.sh --host \
  --datastream /usr/share/xml/scap/ssg/content/ssg-rhel9-ds.xml \
  --profile xccdf_org.ssgproject.content_profile_stig
```

Outputs land in `./oscap-results/` (override with `--outdir`):
`results-<ts>.xml`, `arf-<ts>.xml`, `report-<ts>.html`.

## Reading the exit code (important)

`oscap` exit codes are **scan outcomes, not script errors**:

- `0` — every selected rule passed.
- `2` — the scan ran; **some rules failed** (the normal outcome for an
  un-remediated host — review the report, then remediate).
- other — a real scanner/argument error (the harness re-raises it).

Generate a remediation script from a result (does **not** auto-apply):

```sh
oscap xccdf generate fix --fix-type bash \
  --profile xccdf_org.ssgproject.content_profile_stig \
  --result-id '' oscap-results/arf-<ts>.xml > remediate.sh
```

## Honesty

- We reference the **authoritative** upstream content and give you the harness to
  run it. We do **not** author a custom benchmark or claim a STIG **certification**.
- A passing scan is **evidence**, not an accreditation. There is **no FedRAMP/DoD
  ATO** claimed for this image. See `../docs/SCP-09-FIPS-STIG.md` for the full
  honesty ledger.
