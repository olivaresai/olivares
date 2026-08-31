<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# IPv6 parity — audited declaration (dual-stack and IPv6-only)

> **Status: engineering parity statement, backed by a file-level audit and tests — NOT a USGv6
> Test Program conformance declaration.** Olivares has not been tested by an accredited USGv6
> laboratory (that program targets network IT products in federal acquisitions; see the FAR note
> below). What this page claims is narrower and verifiable: the product **listens, connects,
> parses, stores and displays IPv6 addresses with functional parity to IPv4**, audited
> systematically on 2026-07-09 with the evidence linked per row, and regression-tested.

## Why this page exists (the federal driver, re-verified at the primary source)

OMB Memorandum **M-21-07**, *Completing the Transition to Internet Protocol Version 6 (IPv6)*,
November 19, 2020 — re-read in full from the primary PDF at
`whitehouse.gov/wp-content/uploads/2020/11/M-21-07.pdf` on 2026-07-09:

- Agencies' implementation plans must reach **≥20% of IP-enabled assets operating in IPv6-only
  environments by end of FY2023, ≥50% by FY2024, ≥80% by FY2025** (§4.a-c, p. 3), and all **new**
  networked federal systems must be IPv6-enabled at deployment from FY2023 (§2, p. 3).
- The clause that binds a product like this one (p. 5, *Ensuring Adequate Security*, item 2):
  agencies shall *"ensure that all systems that support network operations or enterprise security
  services (e.g., identity and access management systems, firewalls and intrusion detection /
  protection systems, end-point security systems, security incident and event management systems,
  **access control and policy enforcement systems**, threat intelligence and reputation systems)
  are **IPv6-capable and can operate in IPv6-only environments**"*. Olivares is an access-control
  and policy-enforcement system for AI estates — this clause is why parity is a purchase
  requirement, not marketing.
- The parity bar itself (p. 3, footnote 9): shared services must provide *"full IPv6 support
  (including the ability to function in IPv6-only mode) with **feature and performance parity**
  with existing IPv4 services"*.
- Acquisition (p. 4): the FAR requires requirements documents to reference the **USGv6 Profile
  (NIST SP 500-267)** and USGv6 Test Program declarations, and *"specifying the requirement for
  hardware and software to be capable of operating in an IPv6-only environment"*. We state plainly:
  Olivares has **no USGv6 test declaration**; buyers who need one should say so early
  (see "Honest limits" below).

## What "parity" concretely means here, with the audit evidence

The product is a single Go binary that listens (REST/console + gRPC + auxiliary PEP listeners),
dials dozens of upstreams (connectors), and parses/records addresses (audit, forensics, session
runtime). The 2026-07-09 audit swept all 2,789 Go files plus deploy/packaging; every category below
lists its verdict and the load-bearing evidence. "None found" rows are explicit audit results,
not omissions.

| Category | Verdict (2026-07-09) | Evidence |
|---|---|---|
| Listeners | All `net.Listen`/`ListenPacket` use dual-stack-capable `"tcp"`/`"udp"`; **zero** `"tcp4"`/`"udp4"` anywhere | `cmd/olivares/cmd_serve.go` (serveHTTP/serveGRPC), `core/serverhandover/handover.go:34` (SO_REUSEPORT, family-agnostic), listener connectors (claude, envoy, ssf, cowork, aaa) |
| Container/K8s binds | Fixed by this audit: defaults are `:8443`/`:8444` (Go dual-stack) — `0.0.0.0:P` binds **IPv4 only** in Go and made v6-only pods unreachable | `deploy/helm/olivares/values.yaml`, `deploy/manifests/install.yaml`, `deploy/compose/*.yml`, `operator/internal/controller` (the reconciler), `cmd/olivares/cmd_setup.go`, `INSTALL.md` |
| host:port parsing | `net.SplitHostPort`/`JoinHostPort` everywhere; the four manual-split asymmetries found were fixed in the same pass | fixed: `modules/security/anomaly.go` (bare-v6 egress misclassification), `connectors/a2a/pushrecv.go` (`[::1]` dev exemption), `connectors/mqtt/config.go` + `connectors/kmip/kmip.go` (default-port heuristics); house pattern: `connectors/syslog/syslog.go` |
| URL building | `net.JoinHostPort` (brackets v6); the one string-concat found was fixed | fixed: `connectors/secretref/k8s.go` (IPv6 `KUBERNETES_SERVICE_HOST`); already correct: `connectors/runtime/k8s.go`, `connectors/email`, `connectors/internal/wsclient` |
| Egress gate (isolated runs) | v6-clean: bracket-aware authority parsing, semantic (not textual) IP-rule matching incl. v4-mapped, pinned dial via `JoinHostPort`; CIDR rules accept v6 prefixes | `core/runtime/sandboxrt/proxy.go` (splitHostPort, ipAllowed, pinned dial) |
| IP parsing/validation | `net.ParseIP`/`net/netip`/`net.ParseCIDR` only; **no IPv4-only regexes found** (Go or web console) | e.g. `core/api/authzen_config.go`, `core/api/metrics_config.go`, `core/auth/federation_config.go`, `modules/eventing/dispatch.go` |
| Peer identity / throttling | Client IP always via `SplitHostPort(RemoteAddr)`; API rate-limit keys on principal/tenant, not IP; `X-Forwarded-For` deliberately not trusted | `core/api/handlers_auth.go`, `core/auth/throttle.go`, `core/api/middleware_ratelimit.go` |
| Address rendering/logging | Endpoint refs always `JoinHostPort` (v6 renders bracketed, `tcp://[fd00::1]:443`) | `connectors/ebpf/network.go`, `connectors/internal/meshobs/meshobs.go`, `connectors/envoy/als.go` |
| Storage | **No raw IP columns exist** in the schema (audited); egress log stores host and port as separate fields | `modules/voice/schema.go` (redacted SIP only), `core/runtime/sandboxrt` EgressEvent |
| TLS | Dev/self-signed certs carry both `127.0.0.1` and `::1` SANs; hostname verification works for v6 literals | `core/secure/tls.go:142` |
| gRPC clients | Standard gRPC target resolution (`[v6]:port` supported); TLS ServerName derived via `SplitHostPort` | `cmd/olivares/collector.go`, `connectors/hubble/hubble.go` |

## Tests that pin the parity

- Per-fix regression tests with compressed, bracketed, zoned (`fe80::1%eth0`) and v4-mapped
  (`::ffff:192.0.2.1`) forms in `modules/security`, `connectors/{a2a,mqtt,kmip,secretref}`,
  plus operator/setup default-bind assertions.
- A real-socket E2E suite (`cmd/olivares/e2e_ipv6_test.go`): the nuclear install flow
  (setup → login → org → authenticated read) served over **`https://[::1]`** with certificate
  verification on (no InsecureSkipVerify), a gRPC call over `[::1]`, and a dual-stack `:0` bind
  asserting both `127.0.0.1` and `[::1]` connect.

## Operational notes for dual-stack / IPv6-only deployments

- **Binds.** The engine still binds loopback (`127.0.0.1`) by default on bare hosts —
  secure-by-default exposure, unchanged. In containers/K8s the shipped defaults are dual-stack
  (`:8443`/`:8444`). For a v6 loopback-only posture use `[::1]:8443`; compose host mappings accept
  `[::1]:8443:8443`; the systemd unit's `--listen` accepts the same forms.
- **Egress allowlists are family-explicit by design.** The isolated-run egress gate resolves a
  destination once and requires **every** resolved IP to be allowlisted (anti-rebind,
  fail-closed). A dual-stack destination therefore needs BOTH its IPv4 and IPv6 ranges (or an
  exact host rule) in the allowlist — a v6 AAAA record appearing on a previously v4-only host is
  a *denial*, not a silent widening. This is deliberate; plan allowlists per family.
- **DNS.** Resolution uses the platform resolver (Go net); AAAA-only names work wherever the
  host's resolver returns them.

## Honest limits (declared, not hidden)

- **No USGv6 Test Program declaration** (NIST SP 500-267 conformance is lab-tested, not
  self-asserted). If your acquisition requires it, that is a real gap today.
- **Validation environment.** The E2E suite exercises `::1`/dual-stack sockets. It has not been
  run on a routed IPv6-only network (RA/DHCPv6, AAAA-only DNS end-to-end) as part of CI; nothing
  in the audit suggests an asymmetry there, but we only claim what we test.
- **Upstream dependencies.** A connector's remote endpoint may itself be v4-only (their side,
  not ours); the connector config accepts whatever address family the upstream publishes.
- Loopback-v4 defaults on bare hosts (above) are a security default, not a v6 gap; `::1` binds
  are first-class.

*Audit and fixes: 2026-07-09, evidence per row above. M-21-07 quotes verified
against the primary PDF the same day.*
