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
| `host.hostname` | Complete static kernel hostname, 1–64 ASCII bytes with DNS-shaped labels of 1–63 letters/digits/hyphens. No leading/trailing hyphen, trailing dot, localhost or wholly numeric final label; IP addresses and all-digit names are refused. Lowercased in the plan. |
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
The hostname is the complete static kernel value expected after cloud-init,
including any dotted labels. It is not cloud-init's separate FQDN metadata: the
tool neither takes the first label nor truncates a longer name. A future adapter
must compare the complete observed hostname to this normalized value. The console
origin may be a separate DNS name or proxy. Neither is resolved or compared to live
machine settings by this tool.

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
sorted to detect and reject duplicates. DNS names are lowercased; HTTPS port 443
and the optional trailing slash are omitted. Output preserves the asserted source label.

Go callers use `answers.Build(io.Reader)` followed by `Plan.JSON()`. The module is
in `go.work`, so workspace build/test/vet sweeps include it. Run its bounded tests
with `task appliance:test:answers`. The implementation uses the standard library
and imports no product runtime or host adapter.

## Base package and first boot

`olivares-appliance-base` is the layer every appliance recipe installs beside the
product package (`packaging/nfpm/olivares-appliance-base.yaml`). It installs
`/usr/bin/appliance-firstboot`, `/usr/bin/appliance-answers`, the units
`olivares-appliance-firstboot.service` and `olivares-appliance-readiness.service`,
the console banner `/etc/issue.d/olivares-appliance.issue`, the state directory
`/var/lib/olivares-appliance` and the directory
`/etc/systemd/system/olivares.service.d` for the drop-in first boot writes. It owns no
path of the product package and recommends `olivares`, `cloud-init` and
`openssh-server` (first boot records the instance's SSH host keys). Installing it
enables the first-boot unit and starts nothing, so it installs into an image root the
same way it installs onto a host. Removing it disables the unit and keeps the
first-boot record, the configuration first boot generated and the product; a
reinstallation enables the unit again if the removal found it enabled.
Build and test with `task appliance:build:firstboot`, `task appliance:package:base`
and `task appliance:test:base`; the `appliance-a1` workflow installs the package in
a Debian 13 container with systemd and cloud-init and runs
`appliance/layer/base/fixture/firstboot-battery.sh`. A container is component
evidence: it qualifies no image, hypervisor import, firmware or Secure Boot.

### Ordering and hardening

Debian 13's `cloud-final.service` and `cloud-init.target` are ordered after
`multi-user.target`. The first-boot unit is wanted by `multi-user.target` and runs
after `cloud-final.service`, so it sets `DefaultDependencies=no` and restates a
service's default dependencies: `multi-user.target` does not wait for it, and no
ordering cycle exists. It is not ordered before the product, which only first boot
enables. Both units carry the product unit's hardening directives with the same
values, except `ProtectHome=read-only` (the authorized SSH keys are verified),
`CapabilityBoundingSet=CAP_DAC_READ_SEARCH` (first boot reads other accounts' keys and
the product's data directory as root) and `UMask=0077`; each unit states why. They
may write only `/etc/olivares` and the drop-in directory.

### Carriers

First boot reads one `appliance-answers/v1` document from these carriers and
refuses, naming them, when present carriers disagree:

| Carrier | Where the document is |
| --- | --- |
| `nocloud` | `/etc/olivares-appliance/carriers/nocloud.json`, written by cloud-init from the NoCloud seed's `write_files` (see `answers/carriers/testdata/nocloud/user-data`, which also sets `fqdn` and `prefer_fqdn_over_hostname` so the kernel hostname is the complete `host.hostname`). |
| `guestinfo` | Property `olivares-appliance-answers`, base64, of the OVF environment in `guestinfo.ovfEnv`, read with `vmware-rpctool` or `vmtoolsd --cmd`. |
| `systemd-credential` | Credential `olivares.appliance.answers`, imported by the unit from the system credentials (SMBIOS type 11, `systemd.set_credential=` or a container manager). |
| `file` | `/etc/olivares-appliance/answers.json`, written by an operator or the local assistant. |

Carriers are compared by the answers module's canonical plan without the `source`
label, so formatting does not matter and any difference in an answer refuses.
`source` is advisory: it states where the author meant the document to travel, it is
neither compared nor part of the restart digest, and the first-boot record's `source`
names the carrier that actually delivered the answers. Writing one carrier name to
`/etc/olivares-appliance/carrier` selects it explicitly. Every carrier input must be a
regular file owned by root and not writable by group or others; links are refused. A
carrier that is gone at a later boot (a removed SMBIOS credential, for example) leaves
the recorded outcome in place; first boot continues when a carrier is present again.

### Stages and status

`appliance-firstboot apply` runs, under a lock in the state directory, the stages
`validate`, `prepare-identity`, `verify-host-settings`, `generate-product-config`,
`initialize-storage`, `prepare-setup-delivery`, `verify-firewall`, `start-services`
and `measure-readiness`. A stage is recorded as in progress before its effect can
begin, and each completed stage atomically in `/var/lib/olivares-appliance/state.json`;
a later run compares the recorded effect with the host instead of repeating it. The
record's `product_may_have_started`, written with start-services' in-progress record
before the product's start can be queued and never cleared, means the product's store
and keys are this instance's: a later refusal, a carrier read error or a drifted effect
does not turn them into an imported installation. The field is additive within schema
v1; a record written without it derives it when read from start-services in progress or
completed, never from a refusal recorded at start-services. A seam, the
answers loader or the identity inspection that is not installed refuses before any
stage runs. cloud-init owns the host settings: first boot waits
for it and verifies the hostname, UTC, the authorized SSH keys and its error-free
completion, and never applies them. The product's own `config generate` writes the
product configuration; the public console address goes to a drop-in the appliance
owns. initialize-storage records the data directory's owner and mode; the product's
first start creates the store, and readiness measures it. First boot never starts the product
until the setup-token delivery and the host firewall are measured, and both seams
refuse by default: the product has no protected setup-token delivery sink yet, and no
firewall owner is selected. Ready requires the product's `/readyz` over this
instance's certificate, polled until it answers ok because the product unit is
`Type=simple` and active before the product has created its files, and then its store
and its identity files: not a `systemctl` exit.

The answers' digest is recorded with the first stage that depends on them,
verify-host-settings. Answers corrected before then simply apply; answers changed
after then are refused. `sudo appliance-firstboot reconcile` is the recovery before
the product starts: it forgets the recorded stages that depend on the answers
(prepare-identity and the instance identity stay), and
`sudo systemctl start olivares-appliance-firstboot.service` runs them again with the
current answers and host, inside the unit, with its credentials and hardening; an
`appliance-firstboot apply` from a shell has neither. The same recovers a stage whose
recorded effect the host no longer matches. Once the product may have started, reconcile refuses and
changes nothing: first boot does not reconfigure a started product.

`appliance-firstboot status` (root: `sudo appliance-firstboot status`) prints the
record and exits 0 when ready, 1 when refused and 2 when pending, applying or
unrecorded. `apply` exits 0 when ready or pending, 1 when refused and 2 when it could
not run; `reconcile` exits 0 when it reconciled, 1 when refused and 2 when it could not
run. A record written by another version (schema other than
`olivares-appliance-firstboot/v1`) is refused by name with exit 1 and never rewritten:
a newer version migrates older records forward, and an older version refuses a newer
record until the newer package is installed again. `appliance-firstboot plan` prints
the plan of the document the carriers deliver, and `appliance-firstboot
check-template ROOT` exits 1 naming every instance identity under an image root (SSH
host keys, machine ID, the product's TLS key, setup token, audit, catalog and policy
signing keys and store, and a first-boot record): an initialized instance is never a
template.
