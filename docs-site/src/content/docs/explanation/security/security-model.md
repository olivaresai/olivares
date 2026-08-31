---
title: "The security model"
description: "The secure-by-design posture behind Olivares AI — why read-first, minimal-data, deny-by-default, and a tamper-evident audit are the load-bearing security decisions, not the threat enumeration."
---

Olivares AI is a security product that runs **inside the customer's own
infrastructure** and builds a map of what every AI agent can reach. That makes it
both highly sensitive and highly valuable to an attacker: a defect in this product is
a breach of the customer's estate. The bar is therefore the highest one, and the
posture is designed to **pass an enterprise pentest and audit from the start** rather
than to be hardened later.

This page explains the **posture** — the security decisions baked into the design and
why they are the way they are. It deliberately does **not** restate the formal threat
model: the STRIDE-by-component analysis and the trust-boundary data-flow live on the
[threat model](/explanation/security/threat-model/) page. Read that page for *what
could go wrong and where*; read this one for *why the architecture is shaped to make
those things hard*.

:::note[Posture, not a recon map]
This documentation describes security posture, not attack surface. It does not
enumerate internal permission strings, secret file locations, or the port layout of a
deployment. Those belong in operator hardening material, not in public docs.
:::

## Read-first: low asymmetric risk

The core **observes**; it does not interpose. The access map is reconstructed from
signals the estate already emits — OpenTelemetry, database audit, cloud audit trails,
and (as a non-cooperative backstop) eBPF — and the collector is **never in the
agent's data path**.

This is a security decision before it is a product one. An inline enforcer that sits
in front of every agent action is a single point of failure: if it stalls or
crashes, it can take production down with it, and it becomes a high-value target
precisely *because* it is in the path. A read-first observer carries the opposite,
**asymmetric** risk profile. If the collector fails, it stops *seeing* — it does not
stop the agent, and it does not break production. The worst-case failure of an
observer is a gap in visibility, not an outage.

The same property neutralises the obvious evasion. The collector runs as a separate,
privileged service **outside the agent's control**, so an agent that disables its own
telemetry does not silence the collector — and the eBPF backstop still records the
action at the kernel level. A known agent that suddenly goes quiet is itself treated
as a signal, not ignored.

## Minimal data: what is not stored cannot leak

The graph stores **relations**, not contents. An edge records that an agent touched a
resource, in which mode (read / write / readwrite), from which signal source, with
what confidence, and when. It does **not** store the SQL it ran, the request body,
the secret, or the PII inside them. Where a value is needed only to deduplicate, the
product keeps a one-way hash, never the value itself.

The governing principle is blunt: **what is not stored cannot leak.** The single most
sensitive asset in the system — the access map — is also the one deliberately built
out of the least sensitive data.

The fields most likely to carry secrets or PII (a tool input, a full command) are
**redacted before they are persisted**. Redaction is not left to the handler's good
behaviour: the engine enforces it on the write path, replacing a value marked
sensitive with a hash before it is ever written, as a backstop even if a handler
forgets. The collector reads **identities** — a database role, an application name, an
IAM principal — not credential values or payloads. It is not a data sniffer.

:::note[Coverage is tiered, and the product says so]
Read/write fidelity depends on what the underlying store exposes. It is high on
stores with native audit (SQL, object storage, warehouses), lossy on some
document/vector stores, and **impossible to reconstruct passively** on others. Where
read versus write cannot be determined the edge is marked `unknown`, and attribution
collapses to `approximate` when a shared service account hides per-agent identity. The
product shows these honestly rather than fabricating certainty — see
[honesty & limits](/start/honesty-and-limits/).
:::

## Opaque, revocable tokens over JWT

Authentication uses **opaque bearer tokens**, not JWTs. The token is a random handle;
all authority lives server-side, bound to a record the engine controls. This is a
posture choice. A self-contained JWT is a standing, offline-verifiable bearer of
claims that is awkward to revoke before expiry; an opaque token is **revocable
immediately** by invalidating its server-side record, carries no embedded claims to
leak or to mis-trust, and keeps the tenant binding under the engine's control rather
than in a signature the client holds. Session and API tokens are distinct kinds, and
the tenant is resolved from the token's own binding — a request whose tenant header
contradicts its token is **rejected**, not reconciled.

## No default credentials, one-time setup token

The most common failure of a self-hosted product is a **default credential**. Olivares
AI ships with **none**. On first boot the engine prints a **one-time, single-use setup
token** to standard output; the administrator uses it to create the first user, and
then it is spent. There is no built-in account, no shared password, and nothing to
forget to change. (A demo seed exists for evaluation only; it carries a public
password and **refuses to bind to anything but loopback** so it can never become a
production foothold.)

## Deny-by-default authorization, an ABAC seam that only restricts

Authorization is **deny-by-default**. Role-based access control grants nothing it is
not explicitly told to grant. On top of RBAC sits an attribute-based policy seam — the
operator can run an embedded pure-Go policy engine, an external policy service over
HTTP, or neither, all behind one interface — and the critical invariant is that **the
ABAC layer can only narrow access, never widen it.** A policy can take permission
away; it can never grant a permission that RBAC did not already allow. That ordering
means a misconfigured or overly permissive policy cannot become a privilege-escalation
path: the worst a bad policy can do is lock people out, not let them in.

## Viewing the graph is a privileged, tenant-scoped, audited action

Because the access map is a powerful reconnaissance tool, the design treats **reading
it as a privileged action**, not a default capability. It is granted from an
editor-level role upward and is **never** available to the lowest viewer role. Every
read is **scoped to the tenant** — one customer can never see another's estate — and
**every read is recorded in the audit ledger**: who looked at which agent's access
map, and when. Defence is layered here on purpose: privilege, tenant isolation, and
self-audit together, so that even legitimate access to the most sensitive view leaves
an accountable trail.

This is also where the product's responsible-use line is drawn. Olivares AI is framed
**defensively** — it helps defenders see and govern their own estate. It is not a
command-and-control framework and does not scan other people's credentials. That line
is kept explicit in the [threat model](/explanation/security/threat-model/).

## Append-only, hash-chained, signed audit — with external export as the real control

The audit ledger is **append-only** and **hash-chained**: each record carries the
hash of the one before it, so any silent alteration breaks the chain and is
detectable. On top of the chain, the engine produces **Ed25519-signed** checkpoints,
so the tail cannot be rewritten without the signing key.

The product is honest about the limit of an on-box ledger: an attacker with full
control of the data directory and the on-box key could in principle re-sign a forged
chain. The per-event signature defends against the **database-only** compromise —
injection, a stolen backup or replica, a row-level-security bypass — and against
deletion of checkpoints; it does not, by itself, defend against total host compromise.

So the **real anti-tamper control is external**. The ledger is exported to a
**WORM/SIEM** system the customer controls, in standard formats (`cef`, `leef`,
`syslog`, `otlp`, `otlp_envelope`, `otlp_log_record`, `ocsf`), carrying
sequence number, previous hash, hash and
signature, and **never PII**. Once a copy lives in immutable storage outside the
product, an attacker who compromises the Olivares host cannot reach back and rewrite
what the SIEM already holds. That immutable external copy — not the on-box chain
alone — is what an enterprise auditor asks for, and it is what native telemetry does
not give.

:::note[Two paths off-box: pull, and a real push]
The verifiable ledger reaches a SIEM two ways. The **pull** export (`GET
/v1/audit/export`) is always available and is the artifact an operator archives. A
**push** is real when configured: an `audit.recorded` eventing subscription starts a
per-tenant ledger pump that walks each sealed record and delivers it **at-least-once**
through the durable, SSRF-guarded, retrying/dead-lettering transport
(`modules/siemforward/forwarder.go`, wired in `cmd/olivares/boot.go`). `NopForwarder`
is what applies when no forwarding is configured — not the only implementation that
exists. The [Splunk how-to](/how-to/forward-audit-to-splunk/) documents both paths;
signature verification happens off-box, against the public key.
:::

## TLS on by default, no plaintext fallback, mTLS for remote collectors

Transport is **encrypted by default and fails closed**. TLS is on, and there is **no
silent fallback to plaintext** — a connection that cannot be secured is refused, not
downgraded. A plaintext mode exists strictly for localhost development and must be
asked for explicitly; it is never the default and never the production path.

In the distributed topology, remote collectors **push** to the central core (there is
no inbound listener on the production host, which keeps the collector's open-port
surface at zero), and that channel can require **mutual TLS** with a verified client
certificate. Encryption at rest is provided by the deployment — full-disk, filesystem
or database-level encryption — rather than by a product-level pragma, with strict file
permissions on the data directory.

## License is attestation only — the open core is never gated

The commercial license is verified **offline** with an Ed25519 signature, and in the
**open (AGPL) core** it is an **attestation, not a feature gate**: nothing in the open
product switches off on a license check, ever. Commercial add-ons are licensed for a
paid term — a right that ends with the term — but any consequence of that is a local,
offline decision in the commercial build; there is no remote kill switch, and verifying
the license never calls us. Downloading what you paid for does: the subscription is the
credential with which the commercial add-ons, their updates and their patches are fetched
— the SUSE/Novell model, described in [self-hosting](/how-to/self-hosting/). This matters for the air-gapped
case especially: the product must keep doing its security job — observing, recording,
auditing — regardless of license state, because a security control that quietly
degrades on a license problem is itself a vulnerability. Revocation is handled through
subscription expiry, not by crippling the running engine.

## Self-hosted: what crosses the customer perimeter is what the customer configures

The strongest structural property of the design is that there is **no mandatory
telemetry and no control-plane egress by default**: what crosses the customer's
perimeter is what the customer configures to cross it — calls to their model APIs,
the SIEM/webhook outputs they wire, an external embedding provider if they provision
one. Olivares AI runs on the customer's own hosts; the
data plane (the collectors) **always** runs on customer infrastructure; and there is
**no telemetry-home** — nothing is sent back to Olivares AI as a side effect of running.
The vendor is reached only when the customer asks it for something — `olivares upgrade`, or
a subscription download of commercial add-ons and their updates — and the vendor does not see
the customer's access map.

That is a direct, defensible answer to **GDPR and data-residency** requirements: every
crossing is one the customer provisioned, so residency is theirs to determine and to
evidence rather than ours to grant. And it
makes the **air-gapped** topology a first-class deployment — all local, **zero
egress**, offline license — rather than an afterthought, for estates that must run with
no outbound network at all. See the [self-hosting](/how-to/self-hosting/) and
[air-gap install](/how-to/air-gap-install/) guides.

:::tip[Design for audit, certify later]
The architecture is built to **map onto** the controls SOC 2, ISO 27001 and the EU AI
Act look for — audit logging, access control, integrity, encryption, change
management — so that it passes review when the time comes. Formal certification is a
later, separate step; the design enables it, it does not claim it. The
[honesty & limits](/start/honesty-and-limits/) page is the binding contract on what is
built today versus designed.
:::

## Why these decisions hang together

None of these choices stands alone. Read-first keeps the product out of the blast
radius of the very systems it watches. Minimal-data shrinks what a breach of the
product could even expose. Opaque tokens, no default credentials, deny-by-default RBAC
and a restrict-only ABAC seam mean authority is small, revocable, and impossible to
accidentally widen. The hash-chained, signed, externally-exported ledger makes the
product's own honesty **verifiable** rather than merely promised. And self-hosting
means no mandatory telemetry and no control-plane egress by default: what crosses the
customer's perimeter is what the customer configures to cross it — their model APIs, the
SIEM/webhook outputs they wire, an external embedding provider if they provision one.
The posture is the security
argument; the [threat model](/explanation/security/threat-model/) is where each of
these is checked against a concrete threat.
