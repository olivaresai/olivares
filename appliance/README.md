<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Appliance answers

The offline answers tool validates one document and describes the work needed for
a Community installation. It does not install or configure the machine. A valid
plan has `state: planned`, `executable: false`, and pending prerequisites; it does
not establish that cloud-init completed or that Olivares is ready.

Build with `task appliance:build:answers`, then run:

```sh
bin/appliance-answers validate --input appliance/answers/testdata/cloud-init.json
bin/appliance-answers plan --input appliance/answers/testdata/cloud-init.json
cat appliance/answers/testdata/cloud-init.json | bin/appliance-answers plan --input -
```

Select exactly one file or stdin. Multiple inputs and repeated `--input` options
are refused. The tool does not discover, merge or ingest NoCloud, guestinfo,
systemd credentials or assistant state. The document's `source` labels asserted
provenance only; it does not verify a carrier or assign host authority.

Exit 0 means valid answers or successfully emitted plan; 1 means refused answers;
2 means usage, input or output failure. Successful output goes to stdout and
diagnostics to stderr. Diagnostics identify a schema path and reason, without
echoing submitted values or unknown field names. No apply command exists.

## Document contract

Every field below is required. There are no implicit deployment defaults. Unknown
fields, duplicate object keys, nulls, wrong types and extra JSON documents fail.
Input must be UTF-8 and at most 64 KiB; nesting is bounded to eight levels and
arrays to sixteen entries, with narrower limits below. Objects use exact field
names. Values are strings except objects and the two arrays shown in the example.

| Path | Accepted values and meaning |
| --- | --- |
| `schema_version` | `appliance-answers/v1` only. |
| `source` | One of `file`, `nocloud`, `guestinfo`, `systemd-credential`, `local-assistant`; asserted provenance, not discovery. |
| `host.owner` | `cloud-init` only. A local OS management adapter is not implemented. |
| `host.hostname` | ASCII DNS name, 1–253 bytes; labels 1–63 letters/digits/hyphens, no leading/trailing hyphen or trailing dot; localhost is refused. Lowercased in the plan. |
| `host.network.mode` | `dhcp` only; fixed addresses, gateways and DNS settings are not supported in this version. |
| `host.time.timezone` | `UTC` only. |
| `host.time.servers` | Array of 1–8 unique DNS names or unicast IP addresses; no loopback, link-local, multicast, zone identifiers or localhost. Names are lowercased and IPs normalized. |
| `host.ssh_authorized_keys` | Array of 1–16 unique `ssh-ed25519` public keys. Each has exactly the algorithm, one space and its standard base64 SSH wire value; the wire algorithm and 32-byte key length must agree. Options, comments, other algorithms and private keys are refused. |
| `product.storage_profile` | `single-node-prod`, the existing SQLite configuration profile. `eval`, `postgres-prod` and `k8s` are unsupported here. |
| `product.public_console_url` | HTTPS origin: DNS name or unicast IP, optional port 1–65535 and optional trailing slash. No credentials, query, fragment, other path, percent escapes, whitespace, localhost or loopback/link-local/multicast IPs. It is not contacted or resolved. |
| `product.update_channel` | `stable`, `security` or `lts`, the product's existing channels. |
| `product.node_role` | `control` only. Distributed `inference` is not implemented. |

The checked-in [example](answers/testdata/cloud-init.json) uses a synthetic public
SSH key for validation; supply your own public key for installation planning.
The hostname identifies the instance; the console origin may be a separate DNS
name or proxy. Neither is resolved or compared to live machine settings.

Only public settings are accepted. Passwords, tokens, private keys and literal
DSNs have no field in this schema. PostgreSQL and protected secret references need
a later extension; the tool does not resolve or hash any secret material.
An error always returns no usable plan.

## Plan contract

`appliance-plan/v1` contains the normalized answers, these ordered operations and
the pending prerequisites. Each operation has state `pending`:

| Owner | Action | Meaning |
| --- | --- | --- |
| `cloud-init` | `wait-for-completion` | A future adapter must observe successful completion of the external host owner. |
| `cloud-init` | `verify-host-settings` | A future adapter must verify declared hostname, network, time and public SSH prerequisites. It must not reapply them. |
| `olivares` | `generate-product-config` | A future adapter must use the product's configuration generator. |
| `olivares` | `initialize-storage` | A future adapter must use the product's initialization mechanism. |

Host/product adapters, instance identities, firewall policy, protected setup-token
delivery and expiry, service start and health are pending. These declarations are
not an executable installation sequence, and no prerequisite has been measured.
Packages, first boot, restart recovery, clone isolation and VM support require
their own implementation and verification.

Canonical JSON has a fixed object order and terminal newline. Object key order in
the input does not affect output. Time-server and SSH-key arrays are sets and are
sorted after duplicate checks. DNS names are lowercased; HTTPS port 443 and the
optional trailing slash are omitted. Output preserves the asserted source label.

Go callers use `answers.Build(io.Reader)` followed by `Plan.JSON()`. The module is
in `go.work`, so workspace build/test/vet sweeps include it. Run its bounded tests
with `task appliance:test:answers`. The implementation uses the standard library
and imports no product runtime or host adapter.
