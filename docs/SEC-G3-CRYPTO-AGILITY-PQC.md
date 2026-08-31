<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Crypto-agility and post-quantum posture (PQC)

Crypto-agility posture document, built on top of the existing FIPS-mode build
(**SCP-09**, `docs/SCP-09-FIPS-STIG.md`) and the BYOK/HYOK/CMEK custody layer.

> **Honesty rule** (the same one from SCP-09): only what is verifiable is
> asserted. Nothing in this document asserts a CMVP validation of the product, a
> FedRAMP/ATO authorization, or "CNSA 2.0 compliance". Each assertion carries its
> source and its verification date. **Current decision (2026-06-09):** FIPS-mode
> build + crypto-agility NOW; formal CMVP validation NO (it would only be
> reopened in the face of a gov deal that requires it); PQC = roadmap + ML-DSA
> signing option.

## 1. Cryptographic inventory (where each algorithm lives)

| Surface | Algorithm today | Where it is chosen (the agility point) |
|---|---|---|
| Per-event ledger signature | Ed25519 (on-box, hot path) | `core/audit/eventsig.go` (domain `olivares.audit.event.v1`); verification candidates: `VerifyEventsWith` |
| Checkpoint signature | Ed25519 on-box (default) / **ECDSA P-256·P-384, RSA off-box (HYOK)** | Registry `audit.SigAlg` + `CheckpointVerifier.candidateFor` (`core/audit/offbox.go`) — the ONLY TWO sites to extend for a new alg |
| DEK wrap (CMEK) | AES-256-GCM local + customer KEK (AWS/GCP symmetric, Azure RSA-OAEP-256) | `core/secure/envelope.go` + `core/secure/kmswrap` (versioned format `olivares_sealed: 1`; records provider + KEK — the wrap alg is implied by the backend) |
| TLS (API, gRPC ingest, collector mTLS) | TLS ≥1.2; on 1.3 **hybrid post-quantum KEM by default** (§2) | `core/secure/tls.go` — `CurvePreferences` is left to the Go defaults ON PURPOSE (the tests that pin it: `core/secure/pqc_test.go` for the seam, `cmd/olivares/pqc_listener_test.go` for the configs actually served) |
| Artifact signature (catalog, modelsign) | Ed25519 | `core/secure/modelsign`, catalog key (`cmd/olivares/auditkey.go`) |
| Build supply-chain | cosign keyless, SHA-256, SBOM | `.goreleaser.yaml` |

The "crypto-agility / PQC key-inventory" key inventory view remains
**deferred** — this document does not re-reach it.

## 2. What ALREADY is post-quantum today (fact, not roadmap)

**Transport key establishment.** The Go 1.26 toolchain negotiates by default
in TLS 1.3 the **hybrid ML-KEM** exchanges — `X25519MLKEM768` (preferred),
`SecP256r1MLKEM768`, `SecP384r1MLKEM1024` — when both ends support them.
The engine's TLS configs do **not** pin `CurvePreferences`, so those
defaults apply to the API, to the gRPC ingest, and to the collector mTLS. Two
tests do a real handshake and require `ConnectionState.CurveID ==
X25519MLKEM768`:

- `core/secure/pqc_test.go` — the seam's own configs.
- `cmd/olivares/pqc_listener_test.go` — **the configs the engine actually serves
  with**: what `configureHTTPServerTLS` produces for the REST listener (and every
  auxiliary one) and what `secure.ServerTLSConfigWithLoader` hands to
  `grpc.Creds`. Added 2026-08-06, because the first test alone was NOT the gate
  this paragraph claimed: pinning `CurvePreferences` in `tlsreload.go` — the config
  the live HTTP server consumes — left it green, as an adversarial review
  demonstrated by mutation. Verified by mutation on both composition paths; each
  dies naming the group.

This covers the **harvest-now-decrypt-later** risk
(recording traffic today, decrypting it with a quantum computer tomorrow), which
is the PQC risk with a real clock.

- ML-KEM = FIPS 203 (final since 2024-08-13). The PQ/T hybrid is permitted by
  NIST (SP 800-227 §4.6, final 2025-09-18; combiner with at least one approved
  KEM). The TLS codepoint `X25519MLKEM768` (4588) is in the IANA registry
  with `Recommended=Y`; the draft `draft-ietf-tls-ecdhe-mlkem` is in IESG
  evaluation (not yet an RFC as of 2026-06-10).
- **In FIPS-mode** (`GODEBUG=fips140=on`, image `Dockerfile.fips`): the three
  hybrids are in the FIPS allowlist of `crypto/tls` (bare X25519 is
  excluded; the approved ML-KEM component is the one that carries the approval — the
  Go stance since 1.25). **ML-KEM is inside the validated module
  v1.0.0/cert #5247** (the Security Policy lists ML-KEM KeyGen/EncapDecap, CAVP
  A6650) — the post-quantum KEM does not force leaving the validated module.
- Version gotcha, already resolved in this repo: under `fips140=only` the hybrids
  were broken from Go 1.25 to go1.26.2 (#78178; fixed in go1.26.3+). This
  repo pins the workspace toolchain to `go1.26.6` (`go.work`) and the core module's
  language directive to `go 1.26.5` (`core/go.mod`); both are past the fix.

**Measured against the running binary, not inferred (2026-08-06).** The
claim above is about the ENGINE's TLS, so it was verified where it is served
rather than in a test that builds its own `tls.Config`: `olivares serve` was
started with default flags and a Go 1.26.5 client handshook both listeners,
reporting `ConnectionState.CurveID`.

| Listener | Negotiated group | TLS |
|---|---|---|
| `127.0.0.1:8443` REST/console | **X25519MLKEM768** (codepoint 4588) | 1.3 |
| `127.0.0.1:8444` gRPC ingest | **X25519MLKEM768** (codepoint 4588) | 1.3 |

With a control, because a probe that reports 4588 no matter what it is pointed at
proves nothing: a client pinned to classical curves negotiates **X25519 (29)** on
the same listener and still connects, and a client offering ONLY the hybrid is
accepted. So the hybrid is genuinely available and preferred, and pinning
`CurvePreferences` in the engine would be visible here as a changed number.

## 3. What is NOT post-quantum yet (and why that is defensible)

**The signatures** (ledger, checkpoints, artifacts, cosign) are Ed25519/ECDSA/RSA —
all vulnerable to a future cryptographically relevant quantum computer
(CRQC). Unlike key establishment, a signature does not suffer
harvest-now-decrypt-later: the risk materializes only when the CRQC exists, and
it affects **future verifications of long-lived artifacts** (a 10-year ledger,
a signed model). The agility is already built (the algorithm registries
in §1 are the only change point); the calendar in §5 says
when to activate it.

## 4. Regulatory clocks (verified 2026-06-10, primary sources)

| Clock | Date | What it really requires |
|---|---|---|
| CMVP: FIPS 140-2 → Historical | **2026-09-22** (acceptance until 2026-09-21; no calendar changes) | New procurements: only **140-3** modules. Covered: the FIPS build uses the 140-3 module #5247 (ACTIVE, validated 2026-04-27, sunset 2031-04-26). |
| CNSA 2.0: NSS acquisition gate | **2027-01-01** | Every **new acquisition** for National Security Systems must be CNSA-2.0-compliant (FAQ v2.1, dec-2024). NOTE: it is a purchase gate, not an algorithm-use deadline. |
| CNSA 2.0: software/firmware signing | prefer 2025 → **exclusive 2030** | LMS/XMSS (SP 800-208) or **ML-DSA-87** ("approved for all signing use cases"); **SLH-DSA is NOT approved for NSS**. |
| CNSA 2.0: use by category | browsers/cloud prefer 2025→2033 · networking 2026→2030 · OS 2027→2033 · total transition 2033/2035 | ML-KEM-1024 is the only approved key-establishment parameter (Cat 5). Note: NSA does **not require** hybrids; it permits them. |
| FIPS 206 (FN-DSA/Falcon) | no public draft as of 2026-06-10 (in NIST internal clearance since 2025-08) | Nothing to adopt yet. |

**Honest position:** this product is not an NSS and does not claim
CNSA 2.0 compliance. The clocks are cited because top-tier buyers (gov/regulated)
use them as a purchase yardstick; the posture above answers what we have today
(hybrid KEM + validated 140-3 module) and what is missing (PQC signatures, §5).

## 5. PQC roadmap (signatures) — the ML-DSA option

Verification support is the constraint that rules: the ledger is verified
**off-box with pure Go and without cloud** (`CheckpointVerifier`), so a new
signing alg needs a verifier in the tree.

| External milestone (status as of 2026-06-10) | Action here |
|---|---|
| **Go 1.27: `crypto/mldsa`** — proposal golang/go#77626 **accepted**, milestone 1.27 (ML-DSA-44/65/87, FIPS 204 + RFC 9881). Today there is NO public ML-DSA in stdlib (only internal to the FIPS module v1.26.0); in x/crypto it does not exist; the most credible third party is cloudflare/circl v1.6.3 (FIPS 204 final). | **Decision: wait for stdlib** — do not bring a third-party crypto dependency into the AGPL core months before `crypto/mldsa` lands. On arrival: add `AlgMLDSA87` to the `audit.SigAlg` registry + `candidateFor`, and the dual-signature verification in modelsign / A2A AgentCards as a **forward-leaning posture** (hybrid classical+PQC signature, never PQC-only at the start). |
| **Customer KMS exposes ML-DSA keys** (verify per provider at adoption time; the `kmssign` backends already declare it as a seam: `aws.go:57`). | Map the provider's SigningAlgorithm in the corresponding backend — the `CheckpointKey` seam does not change. |
| **A Go FIPS module with ML-DSA support becomes CMVP-validated** (v1.26.0 is currently Pending Review, CAVP A8028). v1.26.0 already implements ML-DSA internally. | Re-evaluate the `GOFIPS140` pin (deliberate decision, not drift — see SCP-09 §provenance). With a validated module + `crypto/mldsa`, PQC signing can run INSIDE the validated module. |
| **CNSA 2.0 / gov deal that requires ML-DSA-87** | Activate the previous option by contract; the cost is bounded because the change points in §1 are two registries and the seams already name ML-DSA. |

**What we will NOT do:** claim "PQC-ready" for having the hybrid KEM; adopt
SLH-DSA for surfaces with NSS ambition (not approved); change the default
Ed25519 of the hot path without a regulatory or purchase driver (cost/benefit
documented here).

## 6. Custody crypto-agility

The CMEK layer records **provider, KEK and algorithm per envelope** (versioned
format), with KEK rotation (`keys rewrap`), signing-key rotation with
verifiable history (`keys rotate` + `prior_public_keys` +
`VerifyEventsWith`) and provider migration via rotation. Changing the wrap
cryptography (e.g. a KMS that offers ML-KEM wrap in the future) is adding a
`kmswrap` backend and bumping the envelope version — consumers read the format, not
the provider.

## 7. Primary sources (verified 2026-06-10)

- Go FIPS 140-3 / GOFIPS140 / module status: <https://go.dev/doc/security/fips140>
- CMVP cert #5247 (ACTIVE): <https://csrc.nist.gov/projects/cryptographic-module-validation-program/certificate/5247> · Security Policy (ML-KEM approved inside the module): `140sp5247.pdf`
- CMVP Modules-In-Process (v1.26.0 "Pending Review"): <https://csrc.nist.gov/Projects/cryptographic-module-validation-program/modules-in-process/modules-in-process-list>
- FIPS 140-2→Historical transition: <https://csrc.nist.gov/projects/fips-140-3-transition-effort>
- FIPS 203/204/205 (finals 2024-08-13): <https://csrc.nist.gov/pubs/fips/203/final> · `…/204/final` · `…/205/final`
- SP 800-227 §4.6 (PQ/T hybrids permitted): <https://csrc.nist.gov/pubs/sp/800/227/final>
- CNSA 2.0 FAQ (v2.1; timelines and 2027 gate): <https://media.defense.gov/2022/Sep/07/2003071836/-1/-1/0/CSI_CNSA_2.0_FAQ_.PDF>
- TLS hybrid: draft-ietf-tls-ecdhe-mlkem (IESG eval) · IANA TLS groups (4588 `X25519MLKEM768`, Rec=Y) · draft-ietf-tls-hybrid-design (AUTH48)
- Go: X25519MLKEM768 default since 1.24 (`go.dev/doc/go1.24`); SecP*MLKEM in 1.26 (`go1.26`); permitted in FIPS-mode since 1.25 (commit 6114b69e0c, #71757); `fips140=only` fix in go1.26.3 (CL 759383, #78178); `crypto/mldsa` accepted for 1.27 (golang/go#77626)
