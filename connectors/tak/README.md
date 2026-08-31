<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# TAK posture and governed Cursor-on-Target ingest — the `tak` source connector

`connectors/tak` makes the Olivares AI control plane govern a **TAK** (Team
Awareness Kit) deployment as one more surface. It does two separable things:

1. **TAK Server posture** — read a server's configuration (its configured inputs
   and their protocols/ports, TLS/keystore settings, and certificate-signing
   backend) as minimal-data findings. The **grounded** source is the server's own
   `CoreConfig.xml`, read **offline** from disk; an optional live **version probe**
   of the server's version endpoint is the *only* thing the connector reads over the
   network. With neither a config path nor a probe configured, posture is an honest
   **no-op**: it emits nothing rather than fabricate a clean posture for a server it
   never authenticated to. The connector does **not** read or score TAK federation.
2. **Governed CoT ingest** — receive **Cursor-on-Target** events on the
   connector's own **UDP** and **TCP** listeners and lift each one into a governed
   access edge (plus, where warranted, a finding). This connector is a CoT
   *consumer*: you point a TAK feed or CoT clients at its listen address; it does
   not dial the TAK Server to pull.

It is **read-first**. It never writes to a TAK Server, never joins a federation,
and never re-emits the payload it received.

## Configuration

Every key below is taken verbatim from the connector's shipped descriptor
(`descriptor()` in `config.go`). Secrets are supplied **by reference**, never
inline in a finding or an edge.

| Key | Type | Default | Secret | Meaning |
|---|---|---|:--:|---|
| `core_config_path` | string | — | no | Path to TAK Server's `CoreConfig.xml` (package installs: `/opt/tak/CoreConfig.xml`). This is the grounded, offline posture source. |
| `server_url` | string | — | no | TAK Server base URL (e.g. `https://takserver.example.mil:8443`). Optional: enables a live version probe only. |
| `version_path` | string | `/Marti/api/version` | no | Path of the Marti version endpoint on `server_url`. Configurable because tak.gov's API reference is account-gated. |
| `client_cert` | string | — | **yes** | PEM client certificate for TAK Server mTLS, supplied by reference. |
| `client_key` | string | — | **yes** | PEM private key for the client certificate, supplied by reference. |
| `ca_cert` | string | — | no | PEM CA bundle that signs the TAK Server certificate. Empty uses the host trust store. |
| `posture` | bool | `true` | no | Emit TAK Server posture findings. |
| `request_timeout` | duration | `15s` | no | Per-request timeout against the TAK Server API. |
| `feed_ref` | string | `tak` | no | Stable reference for this CoT feed. It is the `source_ref` a sourcescope binding scopes (`source_type=data`). |
| `cot_udp_listen` | string | — | no | UDP listen address for CoT (e.g. `127.0.0.1:6969`). Empty disables UDP ingest. |
| `cot_tcp_listen` | string | — | no | TCP listen address for CoT open-squirt-close (e.g. `127.0.0.1:8087`). Empty disables TCP ingest. |
| `allow_public_bind` | bool | `false` | no | **Dangerous opt-in:** permits a non-loopback CoT listener or multicast join. These bearers are plaintext and unauthenticated, so off-host peers can read or inject position reports. |
| `cot_multicast_group` | string | — | no | Optional multicast group to join on the UDP listener (TAK's SA default is `239.2.3.1`). |
| `cot_max_event_bytes` | int | `65536` | no | Maximum bytes for one CoT event. |
| `cot_max_detail_bytes` | int | `32768` | no | Maximum bytes for the opaque `<detail>` span of one CoT event. |
| `cot_rate_limit_eps` | int | `500` | no | Maximum accepted CoT events per second across all listeners; excess is dropped and counted. |
| `cot_max_tcp_conns` | int | `128` | no | Maximum concurrent TCP CoT connections. |
| `cot_uid_mode` | string | `hash` | no | How a CoT uid leaves the connector: `hash` (default, one-way) or `raw`. A uid identifies a device, and a device identifies its bearer. |

Deny-closed rules enforced at `Open` (in `config.go`): `server_url` must be
`https`; with `posture` on and a `server_url` set but **no** `client_cert` /
`client_key`, the connector **refuses to start** (`ErrPostureUnauthenticated`)
rather than probe a TAK Server anonymously; `cot_multicast_group` requires
`cot_udp_listen` and must be a valid multicast IP. A non-loopback listener or any
multicast join is rejected unless the operator explicitly sets
`allow_public_bind=true`.

### Wiring

```jsonc
// OLIVARES_SOURCES_CONFIG
{
  "sources": [
    {
      "name": "tak-edge",
      "kind": "tak",
      "tenant": "<tenant-uuid>",
      "config": {
        "core_config_path": "/opt/tak/CoreConfig.xml",
        "cot_udp_listen": "127.0.0.1:6969",
        "cot_tcp_listen": "127.0.0.1:8087",
        "feed_ref": "tak"
      }
    }
  ]
}
```

## Ports

These are **TAK Server's** conventional input/listener ports, cited from the
**TAK Server Configuration Guide v5.2** so you know what you are integrating with.
The connector's *own* listeners bind whatever `host:port` you put in
`cot_udp_listen` / `cot_tcp_listen` (the examples reuse the same numbers only for
familiarity).

| Port / group | Convention (TAK Server Configuration Guide v5.2) |
|---|---|
| **8089** | TLS CoT streaming input — the authenticated client↔server channel. |
| **6969** + multicast **239.2.3.1** | Situational-awareness (SA) multicast group. |
| **8087** | Conventional input port; the guide's own canonical example binds it as **UDP** (`<input … protocol="udp" port="8087">`). The port is **protocol-configurable** — 8087 is **not** inherently TCP. |
| **8088** | `stcp` — unencrypted TCP input, **testing only**. |
| **8443** | Administrative web UI. |
| **8446** | Certificate enrollment. |

## Privacy — coordinates and detail never leave the connector

CoT is a position-reporting protocol, so it is the most PII-dense signal this
product ingests. The minimal-data doctrine is applied hard (see `ingest.go`):

- The `lat` / `lon` / `hae` of the `<point>` **never leave this connector**. A
  coordinate is the location of a person. The product records *that* an event was
  received, from *which* emitter, of *which* CoT type — never where anybody is.
- The opaque `<detail>` span never leaves the connector; only its **size** and a
  **SHA-256 digest** are kept, so identical payloads can be correlated without the
  payload being transmitted or stored.
- The emitter's `uid` is **hashed by default** (`cot_uid_mode=hash`, a
  domain-separated one-way digest). Set `cot_uid_mode=raw` only if an operator
  explicitly needs the raw device id on the access map.

## Confidence — a CoT uid is not an authenticated identity

Base CoT carries **no authentication**: any host that can reach a listener may
assert any `uid`. TAK Server's transport security is TLS between the client and
the **server** (port 8089), which says nothing about an event this connector
receives on its own plain UDP/TCP listener. Therefore **every** edge derived from
a base CoT listener is graded **`approximate`** (`confidenceFor` in `ingest.go`
has no code path that returns `attributed`, by design). Read plainly: **a CoT
`uid` is not proof of who sent the event.** It would only become authenticated if
a future listener terminated mTLS and bound the uid to the peer certificate.

## Scoping — govern the feed with a sourcescope binding

The feed is a first-class governed source. A **sourcescope** binding with
`source_type=data` and `source_ref=<feed_ref>` scopes who may use it, on any of
the subject axes **session / agent / user / user_group / role**. Effects are
`allow` (default) or `forbid`, and **`forbid` is absolute** (forbid overrides
allow).

Allow one agent to use the `tak` feed:

```http
POST /v1/m/sourcescope/bindings
Content-Type: application/json

{
  "source_type": "data",
  "source_ref":  "tak",
  "scope_tree":  "agent",
  "scope_ref":   "agent:recon-planner",
  "effect":      "allow",
  "enabled":     true
}
```

Forbid a whole group, absolutely (subtracts even where an allow exists):

```http
POST /v1/m/sourcescope/bindings
Content-Type: application/json

{
  "source_type": "data",
  "source_ref":  "tak",
  "scope_tree":  "user_group",
  "scope_ref":   "group:contractors",
  "effect":      "forbid",
  "enabled":     true
}
```

(`scope_tree` is the subject axis; the full set is `workspace, agent_group,
folder, session, agent, user, user_group, role`.)

## Licence and clean-room provenance

Apache-2.0. The CoT wire format in `cot.go` is a **clean-room** implementation
written from the **public-release MITRE specification only** — no TAK or ATAK
source code was read, copied, translated or derived from:

- *The Developer's Guide to Cursor on Target*, Butler, MITRE, Aug 2005 — DTIC
  accession **ADA637348**, MITRE **Case #06-0249**.
- `Event-PUBLIC.xsd`, the CoT base-event schema (Version 2.0) — MITRE
  **Case #11-3895**.
- *TAK Server Configuration Guide* **v5.2** — for the port/protocol conventions
  above.

`AndroidTacticalAssaultKit-CIV` (ATAK) and `TAK-Product-Center/Server` (TAK
Server) are **GPLv3** and are excluded from this clean-room package by project
policy and provenance review. Both carry a U.S. Federal
**"Distribution A"** release marking, which is a **government release statement,
NOT a software licence** — the licence on both code trees is GPLv3. The CoT
*format* is separable from the TAK *implementations*: MITRE's public-release
schema and developer's guide are what make a clean-room implementation legitimate.
MITRE retains copyright in the schema text itself, so this package implements the
format and cites the source; it does not reproduce the XSD or the guide's prose.
See `doc.go` for the full provenance record.

## Honest limits

- **No mesh/radio bearers.** UDP and TCP only; there is no serial, TAK mesh, or
  radio transport.
- **No ATAK/WinTAK plugins.** This connector does not implement or host any
  end-user TAK client plugin.
- **No TAK federation.** The connector only *observes* that federation is
  configured (from `CoreConfig.xml`); it never federates.
- **No Link-16 / MIL-STD** or any certification-gated tactical protocol, and **no
  Iron Bank / DoD accreditation** — those are separate, optional customer paths.
- **The CoT `<detail>` sub-schema is not modelled.** Only the base event is
  parsed; `<detail>` is treated as opaque, size-capped, digested bytes.
- **UDP loss is uncountable.** Backpressure deliberately slows the listeners; for
  UDP that means the **kernel** drops datagrams before they reach this process,
  and those drops **cannot be counted**. Only events the connector actually
  refused (rate-limit, oversize, malformed, connection-limit) are aggregated into
  rejection findings.

## License boundary

Apache-2.0. The connector imports only the Go standard library and the SDK
(`sdk`, `sdk/model`) — never the AGPL engine (`/core`) or any `modules/` package.
`scripts/check-boundary.sh` enforces the `/core` boundary; excluding `modules/`
is an architectural and provenance rule reviewed separately.
