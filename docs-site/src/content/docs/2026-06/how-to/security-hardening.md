---
title: Harden a deployment
description: "Operator steps to run Olivares AI securely: keep the secure
  defaults, govern destructive actions with human-in-the-loop approvals, verify
  a release before running it, and keep your evidence off-box. Defensive
  posture, by design."
slug: 2026-06/how-to/security-hardening
---

This is the **operator's hardening guide**: the concrete steps to run the control
plane securely. It sits *on top of* the explanatory pages — the
[security model](/2026-06/explanation/security/security-model/) and
[threat model](/2026-06/explanation/security/threat-model/) explain the assets, trust
boundaries and why the posture is what it is. This page is the *how*.

:::note[Defensive by design]
Olivares AI is a defensive product. It helps you **govern your own estate**; it is not
a command-and-control framework and does not scan anyone else's credentials. Reading the
access map is a privileged, tenant-scoped, **audited** action (editor role and up, never
the lowest viewer). This guide hardens the deployment — it does not teach you to map an
estate you do not own.
:::

## 1. Keep the secure defaults

A fresh install is secure by default. The job here is mostly *not weakening* it.

| Default | Keep it because | Operator action |
|---|---|---|
| **No default credentials** | The #1 self-hosted footgun. First boot mints a **one-time, single-use setup token**; you create the first administrator with it. | Read the token from the boot output (or container logs), create the admin, then it is consumed. Never bake a credential into an image. |
| **TLS on by default** | The collector→core and user→panel channels carry sensitive metadata. | Leave TLS on. `--insecure` (plaintext) is **localhost development only** — never on an exposed bind. |
| **Loopback bind** | The engine binds loopback by default so it is never accidentally exposed. | Expose it **deliberately**, behind your own ingress/TLS. In containers the process binds inside the container and the Compose stack maps the host port to loopback — see [self-hosting](/2026-06/how-to/self-hosting/). |
| **No telemetry-home** | A security tool that phones home is a liability. | No action — the engine makes no mandatory outbound calls at boot. In air-gapped mode there is zero egress. |

Every dangerous departure from the defaults is a **named, explicit opt-in** (for
example the development plaintext flag, or allowing a privileged database role). If you
did not set one, it is off. The full secure-defaults posture and the cryptographic
guarantees of the audit ledger are in the [security model](/2026-06/explanation/security/security-model/).

### Mutual TLS for remote collectors

In the distributed topology, edge collectors push observations to the core over
verified-client-cert **mutual TLS**. Turn it on by giving the core a client CA so it
**requires and verifies** a client certificate:

```bash
./bin/olivares serve \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --grpc-client-ca /path/to/collector-ca.pem \
  --data-dir /var/lib/olivares
```

Collectors run on **your** infrastructure with **no inbound listener** (a pure push
model), so they add no open ports to your production hosts. Protect and back up the
data directory (restrictive permissions) — it holds the audit signing key and TLS
material — and keep an off-box copy of the audit public key.

## 2. Govern destructive actions with human-in-the-loop approvals

The control plane is governed by a **deny-by-default** authorization core (RBAC, with an
optional restrict-only Cedar/OPA policy decision point that can only *take access away*,
never widen it). For the model — roles, the policy seam, and the recorded-decisions
guarantee — see [govern and approve](/2026-06/how-to/govern-and-approve/). The operational steps:

1. **Wire the approval gate.** Any module action that would mutate your infrastructure
   (a deployment apply, an orchestration fire, a voice open) passes through a
   human-in-the-loop approval gate that opens a governed approval bound to the exact
   plan, deny-closed and time-boxed. It is enabled by providing the bridge's
   configuration; without it, those actions stay deny-closed.
2. **Use a dedicated approver service account — never a human's.** The component that
   *opens* approvals must run as its **own service account that is never in the approver
   pool**. Separation of duty is enforced engine-side: the identity that opened a request
   cannot decide it, and a system token cannot approve at all. If the opener's account is
   also an approver, you create a liveness deadlock — so keep them separate.
3. **Approvers decide, the ledger remembers.** An authorized human approves or rejects;
   the decision is appended to the tamper-evident ledger with the real actor in the same
   transaction. An expired request can never receive a binding decision. You cannot make
   a governed change the ledger silently forgets.

The approval routes live under the governance module's namespace and are subject to the
same deny-by-default RBAC and per-read auditing as everything else.

## 3. Verify a release before you run it

A control plane is a security product — prove a release is the one the project published
before you run it. The full chain (signature over the checksums, SLSA provenance, SBOM
and OpenVEX attestations, online keyless or fully offline) is in
[verify what you downloaded](/2026-06/how-to/verify-a-release/). The one rule that has no
exceptions:

:::danger[Never `curl | bash`]
Do not pipe an installer into a shell. Download the artifacts, **verify them**, and only
then run them. Deploy container images and the Helm chart **by digest**, never by a
mutable tag.
:::

## 4. Keep your evidence — and your data — at your perimeter

* **Export the ledger off-box.** The append-only, hash-chained, Ed25519-signed audit
  ledger is exposed as an authenticated **pull** export in several SIEM formats, so your
  SIEM or WORM store keeps an immutable copy that re-verifies the chain offline. The
  off-box copy is the real control against a fully compromised host — see
  [forward audit to Splunk](/2026-06/how-to/forward-audit-to-splunk/).
* **Your data never leaves.** The data plane (the collectors) always runs on your
  infrastructure, and the access map stores **relations, never payloads, secrets or PII**
  — minimal-data is a property of the wire, not a setting. This is the structural
  argument for data residency, GDPR and air-gapped operation.

## Related

* [Security model](/2026-06/explanation/security/security-model/) — privilege, tenant scoping, self-audit, minimal-data.
* [Threat model](/2026-06/explanation/security/threat-model/) — assets, trust boundaries, and what each coverage tier can attest.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — the RBAC/PDP model and the approval workflow in depth.
* [Verify what you downloaded](/2026-06/how-to/verify-a-release/) — the full release-verification chain.
* [Self-hosting](/2026-06/how-to/self-hosting/) and [air-gap install](/2026-06/how-to/air-gap-install/) — the deployment topologies.
