---
title: "TAK Server posture & governed Cursor-on-Target ingest"
description: >-
  Govern a TAK deployment: read TAK Server posture from CoreConfig.xml offline
  (with an optional live version probe), and ingest Cursor-on-Target events over
  UDP/TCP as minimal-data governed signal — coordinates and detail never leave
  the connector, every edge honestly approximate.
sidebar:
  order: 9
---

The `tak` source governs a **TAK** (Team Awareness Kit) deployment as one more
surface. It does two separable things, and you can enable either alone:

- **TAK Server posture** — report a server's configuration (its inputs and their
  protocols/ports, TLS/keystore settings, certificate-signing backend) as
  minimal-data findings. The **grounded** source is the server's own
  `CoreConfig.xml`, read **offline** from disk; an optional live **version probe**
  is the only thing read over the network. It does **not** read TAK federation.
- **Governed CoT ingest** — receive **Cursor-on-Target** events on the
  connector's own **UDP** and **TCP** listeners and turn each into a governed
  access edge.

The connector is **read-first**: it never writes to a TAK Server, never joins a
federation, and never re-emits a payload. With no credential and no listener
configured it is an honest **no-op** — it emits nothing rather than fabricate a
posture for a deployment it never contacted.

## What it emits

| Field | Value |
|---|---|
| Signal source | `cot` |
| Mode | `write` — a CoT emitter *contributes* situational-awareness state to the feed |
| Origin | the emitter `uid`, **hashed by default** (`cot_uid_mode`) |
| Confidence | **`approximate`**, always — base CoT is unauthenticated (see below) |
| Findings | drop-track cancellations, unbounded-error events, and aggregated listener rejections (rate-limit / oversize / malformed / conn-limit) |

## 1. Posture: read the server, offline first

The grounded posture source is the server's own configuration file. On a package
install it is `/opt/tak/CoreConfig.xml`. Point the connector at it and it reads
the configured inputs, TLS/keystore settings and certificate-signing backend
**without touching the network**. The `<federation>` element is deliberately not
modelled, so no federation posture is produced.

The live **version probe** is optional and adds only the running version. Because
TAK Server authenticates operators with **mTLS**, the probe is deny-closed: if you
set `server_url` with `posture` on but **omit** the client certificate, the
connector **refuses to start** rather than probe anonymously and report a posture
it did not authenticate. `server_url` must be `https`.

```jsonc
// OLIVARES_SOURCES_CONFIG — posture only
{
  "sources": [{
    "name": "tak-server",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "core_config_path": "/opt/tak/CoreConfig.xml",
      "server_url": "https://takserver.example.mil:8443",
      "client_cert": "${TAK_CLIENT_CERT_PEM}",
      "client_key":  "${TAK_CLIENT_KEY_PEM}"
    }
  }]
}
```

## 2. Ingest: receive CoT over UDP and TCP

Enable a listener and the connector receives CoT — one message per **UDP**
datagram, one message per **TCP** connection ("open-squirt-close"). You point a
TAK feed or CoT clients at the connector's listen address; the connector is the
consumer, it does not dial the server to pull.

```jsonc
// OLIVARES_SOURCES_CONFIG — ingest
{
  "sources": [{
    "name": "tak-edge",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "cot_udp_listen": "0.0.0.0:6969",
      "cot_multicast_group": "239.2.3.1",
      "cot_tcp_listen": "0.0.0.0:8087",
      "allow_public_bind": true,
      "feed_ref": "tak"
    }
  }]
}
```

### Configuration keys (from the connector's shipped descriptor)

| Key | Type | Default | Secret | Meaning |
|---|---|---|:--:|---|
| `core_config_path` | string | — | no | Path to `CoreConfig.xml` (package installs: `/opt/tak/CoreConfig.xml`) — the grounded, offline posture source |
| `server_url` | string | — | no | TAK Server base URL (e.g. `https://takserver.example.mil:8443`). Optional: enables a live version probe only |
| `version_path` | string | `/Marti/api/version` | no | Marti version endpoint on `server_url`. Configurable because tak.gov's API reference is account-gated |
| `client_cert` | string | — | **yes** | PEM client certificate for TAK Server mTLS, by reference |
| `client_key` | string | — | **yes** | PEM private key for the client certificate, by reference |
| `ca_cert` | string | — | no | PEM CA bundle for the TAK Server certificate. Empty uses the host trust store |
| `posture` | bool | `true` | no | Emit TAK Server posture findings |
| `request_timeout` | duration | `15s` | no | Per-request timeout against the TAK Server API |
| `feed_ref` | string | `tak` | no | Stable reference for this CoT feed — the `source_ref` a sourcescope binding scopes (`source_type=data`) |
| `cot_udp_listen` | string | — | no | UDP listen address for CoT (e.g. `127.0.0.1:6969`). Empty disables UDP ingest |
| `cot_tcp_listen` | string | — | no | TCP listen address for CoT open-squirt-close (e.g. `127.0.0.1:8087`). Empty disables TCP ingest |
| `cot_multicast_group` | string | — | no | Optional multicast group to join on the UDP listener (TAK's SA default is `239.2.3.1`). A group join receives from OTHER HOSTS by design, so it requires `allow_public_bind` |
| `allow_public_bind` | bool | `false` | no | **DANGEROUS.** Allow binding the CoT bearers to a non-loopback address, and allow joining a multicast group. CoT is carried in the CLEAR and a CoT event is a position report keyed by a device uid: off-host, anyone who can route to the host can read where the bearers are, and anyone can inject forged positions into the governed feed. Leave it off and keep the bearers on loopback unless the deployment genuinely needs LAN ingest |
| `cot_max_event_bytes` | int | `65536` | no | Maximum bytes for one CoT event |
| `cot_max_detail_bytes` | int | `32768` | no | Maximum bytes for the opaque `<detail>` span of one CoT event |
| `cot_rate_limit_eps` | int | `500` | no | Maximum accepted CoT events per second across all listeners; excess is dropped and counted |
| `cot_max_tcp_conns` | int | `128` | no | Maximum concurrent TCP CoT connections |
| `cot_uid_mode` | string | `hash` | no | How a `uid` leaves the connector: `hash` (default, one-way) or `raw`. A uid identifies a device, and a device identifies its bearer |

## Ports (TAK Server Configuration Guide v5.2)

For context on what you are integrating with. The connector's own listeners bind
whatever `host:port` you configure — the examples reuse these numbers only for
familiarity.

| Port / group | Convention |
|---|---|
| **8089** | TLS CoT streaming input — the authenticated client↔server channel |
| **6969** + multicast **239.2.3.1** | Situational-awareness (SA) multicast group |
| **8087** | Conventional input port; the guide's canonical example binds it as **UDP**. Protocol-configurable — 8087 is **not** inherently TCP |
| **8088** | `stcp` — unencrypted TCP input, **testing only** |
| **8443** | Administrative web UI |
| **8446** | Certificate enrollment |

## Privacy: coordinates and detail never leave the connector

CoT is a position-reporting protocol — the most PII-dense signal this product
ingests — so minimal-data is enforced hard:

- The `lat` / `lon` / `hae` of the `<point>` **never leave the connector.** A
  coordinate is the location of a person; the product records that an event was
  received, from which emitter, of which CoT type — never where anybody is.
- The opaque `<detail>` span never leaves the connector; only its **size** and a
  **SHA-256 digest** are kept, so identical payloads correlate without the payload
  being stored.
- The emitter `uid` is **hashed by default** (`cot_uid_mode=hash`, domain-separated
  and one-way). `raw` is an explicit operator opt-in.

## Confidence: a CoT uid is not an authenticated identity

Base CoT carries **no authentication** — any host that can reach a listener may
assert any `uid`. TAK Server's TLS protects the client↔**server** channel (port
8089); it says nothing about an event this connector receives on its own plain
UDP/TCP listener. So **every** edge from a base CoT listener is graded
**`approximate`**, by design — there is no code path that returns `attributed`.

:::caution[A `uid` is a claim, not proof]
Read a CoT `uid` as *"an emitter claiming this id published into the feed"*, not
as an authenticated identity. It would only become authenticated if a listener
terminated mTLS and bound the uid to the peer certificate.
:::

## Scoping: govern the feed with a sourcescope binding

The feed is a first-class governed source. A **sourcescope** binding scopes who
may use it with `source_type=data` and `source_ref=<feed_ref>`, on any subject
axis — **session / agent / user / user_group / role**. Effects are `allow`
(default) or `forbid`, and **`forbid` is absolute** (forbid overrides allow).

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

Set `"effect": "forbid"` (with, say, `"scope_tree": "user_group"`) to subtract a
whole group's access, even where an allow exists.

## Licence and clean-room provenance

The CoT wire format is a **clean-room** implementation written from the
**public-release MITRE specification only** — no TAK or ATAK source code was read,
copied or derived:

- *The Developer's Guide to Cursor on Target*, Butler, MITRE, Aug 2005 — DTIC
  **ADA637348**, MITRE **Case #06-0249**.
- `Event-PUBLIC.xsd`, the CoT base-event schema (Version 2.0) — MITRE
  **Case #11-3895**.
- *TAK Server Configuration Guide* **v5.2** — for the port/protocol conventions.

ATAK-CIV and TAK Server are **GPLv3** and off-limits to the connector (Apache-2.0),
enforced by the licence boundary check. Both carry a U.S. Federal **"Distribution
A"** marking, which is a **government release statement, not a software licence** —
the code trees are GPLv3. MITRE's public-release schema and guide are what make a
clean-room implementation legitimate.

## Honest limits

- **No mesh/radio bearers** — UDP and TCP only; no serial, TAK mesh or radio.
- **No ATAK/WinTAK plugins** — the connector implements no end-user TAK client.
- **No TAK federation** — it only *observes* that federation is configured; it
  never federates.
- **No Link-16 / MIL-STD** or certification-gated tactical protocol, and **no
  Iron Bank / DoD accreditation** — separate, optional customer paths.
- **The CoT `<detail>` sub-schema is not modelled** — only the base event is
  parsed; detail is opaque, size-capped, digested bytes.
- **UDP loss is uncountable** — backpressure slows the listeners; for UDP the
  **kernel** drops datagrams before this process sees them, and those drops cannot
  be counted. Only events the connector actually refused are aggregated into
  rejection findings.

## Related

- [Connect a source](/how-to/connect-a-source/) — the connector model and the
  honest-tier taxonomy.
- [Govern and approve](/how-to/govern-and-approve/) — the authorization model a
  sourcescope binding plugs into.
- [Connectors & coverage tiers](/reference/connectors/) — the full catalog.
