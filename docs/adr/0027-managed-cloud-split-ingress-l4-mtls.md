<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# ADR-0027: Managed-cloud ingress — L4 passthrough for collector mTLS, L7 for the control-plane API

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0012 (collectors push to the core over gRPC + mTLS), ADR-0028
  (managed-cloud database), ADR-0029 (managed-cloud regions), ADR-0009 (append-only
  hash-chained audit); the platform decision record for the managed cloud; AWS Elastic
  Load Balancing documentation, consulted 2026-08-02:
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/network/network-load-balancers.html`,
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/network/edit-target-group-attributes.html`,
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/application/configuring-mtls-with-elb.html`.

## Context and problem statement

ADR-0012 fixed the ingestion topology: collectors run on customer infrastructure and
**push** observations over gRPC with mutual TLS, and the core **terminates that mTLS
itself**.

It is worth being exact about what that buys, because the loose version of this sentence
is wrong and would be load-bearing if believed. Admission on the collector plane rests on
**two independent factors**:

1. **A transport gate.** The server requires and verifies a client certificate that chains
   to the configured collector CA. This proves possession of a key whose certificate we
   issued; it is not parsed into a subject and it does not name a principal.
2. **A bearer principal.** The authenticated identity that authorization and the audit
   chain (ADR-0009) act on comes from the request's bearer token, not from the
   certificate.

Both are enforced **in the product's own process**. Nothing in between asserts either one.
That is the property this record is about: not "the certificate is the identity", but "no
intermediary vouches for either factor".

The managed cloud is the first deployment that puts a load balancer in front of that
binary. The same deployment also exposes an ordinary public HTTPS surface — REST API,
console, admin — which wants the opposite treatment: a managed public certificate, a web
application firewall, host/path routing. A single ingress cannot serve both without giving
up something on one side.

## Decision drivers

- Both admission factors must keep being enforced by **a TLS session the product itself
  terminates**. A managed cloud that quietly downgrades either one to "an intermediary told
  us it was fine" would weaken the central claim of the product.
- The public HTTP surface should be able to use the edge protections that L7 offers,
  without the product re-implementing them.
- Long-lived collector streams must survive the ingress's idle behaviour.
- No regression against the self-hosted deployment: one code path, not two.

## Considered options

- **A — one L4 load balancer for everything.** TCP passthrough for both planes; the
  binary terminates every TLS session, public API included.
- **B — split ingress.** A **network (L4) load balancer with a TCP listener** for the
  collector plane in passthrough, plus an **application (L7) load balancer** for the
  control-plane HTTP surface.
- **C — one L7 load balancer with managed mutual TLS.** The application load balancer
  authenticates client certificates itself (verify mode against a trust store, with
  revocation lists) or forwards the chain to the target as an HTTP header.

## Decision outcome

Chosen option: **B — split ingress**.

### Consequences

- **Good:** the collector plane is byte-for-byte the self-hosted path. A TCP listener does
  not terminate TLS, so the binary performs the handshake and enforces the certificate
  requirement itself, exactly as it does on-premises. No cloud-specific branch in the
  authorizer, and no cloud-specific case in the audit chain.
- **Good:** the public surface can use a managed certificate, host/path routing and a web
  application firewall without the product re-implementing any of it. The firewall is a
  **separately priced** service, not a free property of the L7 load balancer; it is listed
  here as available, not as included.
- **Good, with its scope stated precisely:** the TCP listener's idle timeout is
  **configurable between 60 and 6000 seconds** (`tcp.idle_timeout.seconds`, default
  **350**); a TLS listener's is **fixed at 350 seconds and cannot be modified**. This is an
  **idle** timeout — absence of bytes — **not a ceiling on stream duration**: a stream that
  keeps sending data or keepalive frames is not cut at 350 seconds. So passthrough does not
  "make long streams possible"; it makes the idle budget ours to set. Stated the other way
  round, because that is the part that matters: **a quiet stream dies on any of these
  ingresses**, and the client must survive that.
- **Bad, and the reason the point above is stated as a warning:** the collector client
  configures **no gRPC keepalive** (the library default is off), and after a failed send it
  keeps the dead stream cached rather than rebuilding it. An idle period longer than the
  configured timeout, a leadership change, or a deployment therefore ends a collector stream
  that nothing reconnects. This is **not created by the split** — it is pre-existing — but
  the split is the first deployment where an intermediary will actively close idle
  connections, so it is where the gap starts costing data. A reconnect-with-backoff loop on
  the collector side is a **precondition** for calling this ingress production-ready.
- **Bad / trade-offs:** two load balancers means two hourly charges and two independent
  capacity-unit meters, which together dominate the fixed monthly floor of a small
  deployment. This is a real and recurring cost paid for keeping both admission factors
  in-process.
- **Bad, and a build requirement rather than a footnote:** for **IP-type target groups
  with the TCP or TLS protocol, client IP preservation is disabled by default** — and
  tasks on the managed container runtime are IP targets. Left at the default, every
  collector connection reaches the binary with the load balancer's private address as
  its source. Anything address-derived — audit records, rate limits, address
  allow-lists — would be silently wrong from the first day. The ingress is not complete
  until either `preserve_client_ip.enabled` is on or the binary parses Proxy Protocol v2
  ahead of the handshake. Enabling preservation also means the target's security group
  faces client addresses rather than the load balancer's, which the network design must
  account for.
- **Neutral / follow-ups:** which of the two mechanisms restores the source address is
  left to the implementation phase, but **the choice must be made and tested, not
  inherited from a default**. A test that asserts the recorded source address equals the
  collector's is the acceptance criterion.

## Why the alternatives were rejected

- **A (one L4 load balancer)** — rejected for the *public* plane, not for the collector
  plane. It is cheaper and it is the closest thing to the self-hosted topology, but the
  control-plane API would lose managed certificates, WAF and host/path routing, and the
  product would end up re-implementing at L7 what the edge already provides. The
  collector half of option A is exactly what option B keeps.
- **C (managed mutual TLS at L7)** — rejected because it **moves the trust boundary**. In
  verify mode the edge performs the certificate check and the application receives a request
  that has already been vouched for; in passthrough mode the certificate chain arrives as an
  `X-Amzn-Mtls-Clientcert` header. In both, the transport gate stops being something the
  product enforced and becomes an assertion made by something else — the precise
  substitution this product exists to make verifiable, and one whose failure mode (anything
  that can reach the target directly can forge the header) is a network-configuration
  mistake away. The managed trust store with revocation lists is a genuine operational
  advantage, and one the product does not currently have for collector certificates at all:
  it loads a CA and performs ordinary X.509 validation, with no CRL or OCSP check. If
  managed revocation ever outweighs first-hand termination, that will be a **new record**,
  not an amendment to this one.
