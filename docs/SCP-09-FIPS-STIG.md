<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# SCP-09 — FIPS 140-3 build variant + STIG/OSCAP-verifiable image

This document is the gate documentation for **SCP-09**: a
FIPS 140-3 crypto build variant and a STIG-hardened image with a published OpenSCAP
profile, shipped as **separate, opt-in artifacts**.

> **The value of this deliverable is being correct and verifiable — not claiming
> certifications we do not hold.** Read the [Honesty ledger](#honesty-ledger) first.
> We claim **no FedRAMP authorization and no DoD ATO**, and we do **not** fabricate a
> CMVP validation that does not exist.

## TL;DR

| Artifact | What it is | How you verify it |
|---|---|---|
| `olivares-fips` binary / archive (`.goreleaser.yaml`) | The control plane built in Go **native FIPS 140-3 mode** (`GOFIPS140=v1.0.0`, still `CGO_ENABLED=0` pure-Go static). | `scripts/fips-verify.sh` — builds it, proves the validated module is linked, demonstrates the runtime toggle. |
| `Dockerfile.fips` → `…:VERSION-fips-amd64` | That FIPS binary on the same distroless static base, launched with `GODEBUG=fips140=on`. | `docker run --rm IMG version` (FIPS on); distroless has **no OS** to OSCAP-scan. |
| `Dockerfile.stig` → `…:VERSION-stig-amd64` | The FIPS binary on a **scannable**, STIG-profiled OS (UBI micro), non-root. | `oscap/scan.sh --image …` against the **upstream** DISA STIG SCAP content. |
| `oscap/` | OpenSCAP harness: `scan.sh`, `tailoring.xml` (extends the upstream profile), `README.md`. | You run it; it produces **your** report. We ship no baked result. |

**The default pure-Go reproducible binary that release packaging ships is
unaffected.** These are additive variants alongside it.

---

## Which compliance gate each artifact serves

- **FedRAMP Moderate/High → CMVP-validated cryptography.** FedRAMP requires the
  cryptographic module to be CMVP-validated. With FIPS 140-2 moved to the CMVP
  **Historical List** in early 2026, new deployments target **FIPS 140-3**. The
  FIPS binary uses a **140-3** module that **is** CMVP-validated (cert #5247). This
  satisfies the *cryptographic-module* prerequisite; it is **not**, by itself, a
  FedRAMP authorization (which is a system-level process we do not claim).
  Authority: *FedRAMP Policy for Cryptographic Module Selection v1.1.0* —
  <https://www.fedramp.gov/resources/documents/FedRAMP_Policy_for_Cryptographic_Module_Selection_v1.1.0.pdf>
- **DoD / federal → STIG hardening with auditable OSCAP verification.** DISA STIG
  is the hardening standard and **OpenSCAP/OSCAP** is the verification path. The
  STIG image bases on an OS that ships DISA STIG SCAP content and is **self-verifiable**
  with `oscap/`. A passing scan is **evidence**, not an accreditation/ATO.
  Authority: DISA STIG + OpenSCAP; public profile source **ComplianceAsCode /
  `scap-security-guide`** — <https://github.com/ComplianceAsCode/content> and
  <https://www.open-scap.org/>

---

## FIPS 140-3: exact build instructions

Go's native FIPS 140-3 mode is selected **at build time** by the **`GOFIPS140`**
environment variable. Authority: <https://go.dev/doc/security/fips140>.

- Accepted values: `off` (default), `latest`, `v1.0.0`, `v1.26.0`, `inprocess`,
  `certified`.
- Native mode uses the in-tree `crypto/internal/fips140` module — it **does not
  require CGO** and **keeps the pure-Go static build** (`CGO_ENABLED=0`). It is
  **incompatible** with the old Go+BoringCrypto. The modules ship **inside the Go
  toolchain** (`$GOROOT/lib/fips140`), so the build needs **no network** and does
  **not** mutate `go.work` / `go.work.sum`.
- Unsupported targets: OpenBSD, Wasm, AIX, 32-bit Windows.
- **Runtime toggle:** `GODEBUG=fips140=on`. Verify in-process with
  `crypto/fips140.Enabled()` / `crypto/fips140.Version()`.

### Build the validated variant

```sh
# Pin v1.0.0 — the CMVP-VALIDATED module (cert #5247). Still pure-Go static.
GOFIPS140=v1.0.0 CGO_ENABLED=0 go build -o olivares-fips ./cmd/olivares
```

This is exactly what the `.goreleaser.yaml` `olivares-fips` build, the
`Dockerfile.fips`/`Dockerfile.stig` go stages, and `scripts/fips-verify.sh` do.

### Why `v1.0.0` and not `v1.26.0`/`latest`

This repo's workspace toolchain is **Go 1.26.6** (`go.work`: `go 1.26.6` /
`toolchain go1.26.6`).
The module matching it is **v1.26.0** — but as of **2026-04-28** that module is
**"Pending Review"** on the CMVP **Modules-In-Process List** (CAVP cert **A8028**) —
i.e. **NOT yet validated**. The **v1.0.0** module (frozen from Go 1.24) holds
**CMVP Certificate #5247** (active/validated). So for an *actually CMVP-validated*
build we pin **`GOFIPS140=v1.0.0`** (equivalently `GOFIPS140=certified`, which on
this toolchain resolves to the v1.0.0 module). `v1.26.0`/`latest` **enable FIPS
mode** but are **not CMVP-validated** — do not represent them as validated.

> `$GOROOT/lib/fips140/certified.txt` resolves to the v1.0.0 module and
> `inprocess.txt` to v1.26.0 on this toolchain — verifiable on disk.

### Verify your build (no network)

```sh
scripts/fips-verify.sh     # or: task fips:verify
```

It (1) builds with `GOFIPS140=v1.0.0 CGO_ENABLED=0`, (2) asserts the
`crypto/internal/fips140/v1.0.0` symbols are linked in, (3) asserts `go.work` /
`go.work.sum` are byte-for-byte unchanged, (4) runs a tiny program under
`GODEBUG=fips140=on` that prints `fips140.Enabled()=true version="v1.0.0"`, and
(5) asserts the **olivares binary itself** self-reports the mode:
`olivares version` ends with `fips140=on module=v1.0.0` under the toggle.
This self-check also runs in **mainline CI** (`fips` job), so a change
that breaks the FIPS build can no longer land silently on `main` (previously it
was only exercised by goreleaser on a `v*` tag).

Manual runtime check:

```sh
cat > /tmp/fipscheck.go <<'GO'
package main
import ("crypto/fips140"; "fmt")
func main(){ fmt.Printf("enabled=%v version=%s\n", fips140.Enabled(), fips140.Version()) }
GO
( cd /tmp && go mod init fipscheck >/dev/null 2>&1; \
  GOFIPS140=v1.0.0 go build -o fipscheck . && GODEBUG=fips140=on ./fipscheck )
# -> enabled=true version=v1.0.0
```

---

**Dependency note (NATS client):** `github.com/nats-io/nats.go` (+`nkeys`, `nuid`) joined
core for the distributed-bus bridge. It introduces **no crypto module of its own**: `nkeys` uses
the stdlib `crypto/ed25519` (inside the validated Go FIPS boundary) and is dormant unless NKey
authentication is configured; TLS to the NATS server rides the same stdlib TLS stack as every
other connection. The FIPS build story above is unchanged.

## STIG / OSCAP: why two images, and how to scan

A DISA STIG is **verified with OpenSCAP** against a SCAP datastream, which needs a
**scannable OS** (package DB, `/etc`, PAM/auditd config…). The default **distroless**
image — and `Dockerfile.fips`, which keeps distroless for a lean FIPS *binary* image
— has **no OS**, so there is **nothing for `oscap` to evaluate** there. That is by
design: distroless removes the very surface STIG checks inspect.

Therefore the **STIG image** (`Dockerfile.stig`) is a separate **deployment** variant:

- Base: **Red Hat UBI micro** (freely redistributable; the matching SCAP datastream
  ships in `scap-security-guide` / upstream ComplianceAsCode for RHEL9). A non-root
  account + owned `/data` are staged in UBI minimal and copied across.
- It carries the **same FIPS binary** (`GOFIPS140=v1.0.0`), runs **non-root**
  (uid/gid 65532), and sets `GODEBUG=fips140=on`.
- **Alternative base (documented, not shipped):** Ubuntu + the **`usg`** Ubuntu
  Security Guide tooling (or the upstream `ssg-ubuntu*` datastream) is an equally
  valid STIG base; swap the final stages and point `oscap/scan.sh --datastream` at
  the Ubuntu datastream.

### Run the scan (you produce the evidence)

```sh
# On the SCANNING host (not in the image):
dnf install -y openscap-scanner openscap-utils scap-security-guide

oscap/scan.sh --image olivares:stig                       # scan the image rootfs
oscap/scan.sh --image olivares:stig --tailoring oscap/tailoring.xml
oscap/scan.sh --host                                                   # scan the host OS
```

Outputs: `oscap-results/results-<ts>.xml`, `arf-<ts>.xml`, `report-<ts>.html`.
`oscap` exit `2` = "ran, some rules failed" (a real outcome to remediate, **not** a
harness error). The content is the **authoritative upstream** DISA STIG profile; our
`oscap/tailoring.xml` only **extends** it and **deselects** container-inapplicable
rules (graphical login, bootloader, USB) with each deselection justified inline — it
**redefines no rule** and **weakens nothing**. FIPS/crypto-policy rules stay selected.
See [`oscap/README.md`](../oscap/README.md).

---

## Honesty ledger

The point of this artifact is to be **truthful and verifiable**. The table below is
the contract: what each artifact claims, and its verification status.

| Artifact / setting | What is claimed | Verification status |
|---|---|---|
| **FIPS binary** `GOFIPS140=v1.0.0` (`olivares-fips`, `Dockerfile.fips`, `Dockerfile.stig`) | Built in Go native FIPS 140-3 mode using the **CMVP-validated** "FIPS 140-3 Go Cryptographic Module **v1.0.0**", **CMVP Certificate #5247** (active; frozen from Go 1.24). | **Validated module.** Self-verify the build with `scripts/fips-verify.sh` (links `crypto/internal/fips140/v1.0.0`) and the runtime with `GODEBUG=fips140=on` → `fips140.Enabled()==true`, `Version()=="v1.0.0"`. We claim the *module* is validated — **not** that any product/system is FedRAMP-authorized. |
| **`GOFIPS140=v1.26.0` / `latest`** (matches this repo's Go 1.26 toolchain) | Enables FIPS 140-3 **mode** with the v1.26.0 module. | **FIPS mode, but NOT CMVP-validated.** v1.26.0 is **"Pending Review"** on the CMVP Modules-In-Process List (2026-04-28, CAVP cert **A8028**). Do **not** represent as validated. We pin `v1.0.0` precisely to avoid this. |
| **`GODEBUG=fips140=on`** (runtime) | The validated module runs its self-tests and the stdlib permits only approved algorithms for this process. | **Verifiable** via `crypto/fips140.Enabled()`. Build-time `GOFIPS140` selects the module; this toggle enforces it at runtime. |
| **STIG image** (`Dockerfile.stig`) | Hardened toward the DISA STIG and **self-verifiable** with the published OpenSCAP profile. | **Self-verifiable, NOT a certification.** Run `oscap/scan.sh` against the **upstream** ComplianceAsCode DISA STIG content to produce **your** report. We ship the harness, not a passing result. **No DoD ATO** is claimed. |
| **`oscap/tailoring.xml`** | A tailoring that **extends** the upstream DISA STIG profile and deselects only container-inapplicable rules. | **Honest tailoring.** Inherits all upstream rules; redefines none; weakens none; each deselection justified inline and reversible. FIPS/crypto rules stay selected. |
| **Default pure-Go binary / distroless image** (base `olivares` build, `Dockerfile`/`Dockerfile.release`) | The standard reproducible release artifact. | **Unaffected by SCP-09.** Byte-for-byte unchanged; the FIPS/STIG variants are additive and opt-in. |
| **FedRAMP / DoD ATO** | — | **Not claimed. None held.** This deliverable provides validated-crypto and self-verifiable-STIG *building blocks*; authorization is a separate, system-level process. |

### Explicit non-claims

- The **product** and any **system** are **not FIPS-validated**; only the
  **crypto module** (#5247) is validated. Module validation ≠ system authorization.
- We do **not** claim **FedRAMP authorization** or a **DoD ATO**.
- We do **not** ship or imply a **passing OSCAP scan**; we ship the harness to
  generate one.
- `GOFIPS140=v1.26.0`/`latest` are **not** validated and are never presented as such.

---

## Primary sources

- Go FIPS 140-3 mode (`GOFIPS140`, `GODEBUG=fips140=on`, `crypto/fips140`):
  <https://go.dev/doc/security/fips140>
- FedRAMP Policy for Cryptographic Module Selection v1.1.0:
  <https://www.fedramp.gov/resources/documents/FedRAMP_Policy_for_Cryptographic_Module_Selection_v1.1.0.pdf>
- ComplianceAsCode / `scap-security-guide` (DISA STIG profiles):
  <https://github.com/ComplianceAsCode/content>
- OpenSCAP project: <https://www.open-scap.org/>
- NIST CMVP (cert #5247 "FIPS 140-3 Go Cryptographic Module v1.0.0"; v1.26.0
  Modules-In-Process / CAVP A8028): <https://csrc.nist.gov/projects/cryptographic-module-validation-program>

## Provenance note (verify, don't trust)

The CMVP cert number (#5247), the v1.26.0 "Pending Review" status (2026-04-28,
CAVP A8028), the `GOFIPS140` accepted values, and the toolchain↔module mapping were
provided as verified primary-source facts for this build (June 2026) and are
reflected in code/comments. CMVP listings change over time — **re-verify against the
CMVP search and `$GOROOT/lib/fips140/*.txt` before representing status to an auditor.**

**Last re-verification: 2026-06-10, against the primary sources:** cert
**#5247** is **ACTIVE** (validated 2026-04-27 by Lightship Security, Overall
Level 1, sunset 2031-04-26) and remains the only validated Go module; **v1.26.0
is still "Pending Review"** on the Modules-In-Process list (list updated
2026-06-10); `GOFIPS140=certified` still resolves to v1.0.0 and `inprocess` to
v1.26.0 on this toolchain. Note the Go docs' lifecycle caveat: older module
versions are removed from the toolchain once a NEWER version obtains a CMVP
certificate — when v1.26.0 (or later) is validated, revisit the pin
deliberately rather than letting `certified` drift. For the crypto-agility and
post-quantum posture built on top of this variant, see
[`SEC-G3-CRYPTO-AGILITY-PQC.md`](SEC-G3-CRYPTO-AGILITY-PQC.md).
