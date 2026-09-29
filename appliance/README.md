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
`/etc/systemd/system/olivares.service.d` for the drop-in first boot writes; its postinstall
creates `/etc/olivares-portal` (root, 0755) when it is missing, for the Appliance Console's
selection. It owns no
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
may write only `/etc/olivares`, the drop-in directory and `/etc/olivares-portal`.

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
owns. When the answers declare `portal.enabled`, `portal.listen` or
`host.management_interfaces`, generate-product-config also publishes them, as validated, to
`/etc/olivares-portal/selection.json` (root, 0644), the one file the Appliance Console reads
its selection from, and records its digest; when they declare none, no selection is published
and one left from earlier answers is removed. initialize-storage records the data directory's owner and mode; the product's
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

## Console sign-in modes

The Appliance Console decides who may ask it for an act from the state of the
appliance, states the mode it is in on every page, and offers nothing that mode
does not offer. It holds no password, hash or credential store of its own.

| Mode | Who authenticates | What is offered |
| --- | --- | --- |
| `product-up` | The product's own identity, with a step-up through the product's existing authorizer. The console asks once and returns the answer unchanged, keeping no decision of its own. | Everything the console has. |
| `product-down-repair` | The host, through the PAM service the layer ships, for a human in the `olivares-admins` local group: who this human is on this host. | The repair verbs only: `apply-update`, `roll-back`, `network`, `certificate`, `support-bundle`, `power`. |
| `unavailable` | Nobody, over the network: the product does not answer and the console's sign-in stack cannot be used, or the product's reachability was never measured. | Nothing. An unmeasured product is not a product that is down, and the page names the remaining door. |

That sign-in is never a session identity and never a provider-account identity, and a
step-up in it is a re-authentication, because no policy engine is running to ask. A human
the host does not place in `olivares-admins` is offered nothing, and their credential is
never presented to the host's stack: the group check runs first. Every refusal is one
phrase, because a refusal that names the check it failed is an oracle.

The remaining door is the **Appliance Repair Console on tty1**, and it is designed as
one. It is not a network service, so the failure that closes the console on 9443 cannot
close it. It authenticates its operator by its own sign-in, through the host's local-console
sign-in service, never by access to this machine: access to the console alone authorizes no
act, and the console keeps no credential store of its own. It carries the same repair verbs
and one more, `repair-portal-pam`, which no other surface can carry, because over the network
that stack is what is broken. In this version neither console performs a verb: the console on
9443 offers the verbs and performs none of them, and the repair console's qualified sign-in
is not implemented yet, so `power` refuses like `certificate` and `repair-portal-pam`, states
why, and asks nothing of the power helper (below).

The repair console exits 0 when the operator leaves with `q` or its input ends. It exits 2
on an input or output failure: a read that fails, a line longer than it reads, or output it
cannot write. It then stops and states one fixed sentence on stderr, which its unit sends
to the journal. No selection is refused, so it never exits 1.

Neither the product probe nor the host's sign-in stack is wired in this version, so the
mode a page states today is `unavailable`. The PAM service file and the tty1 unit are
packaged and enabled by nothing: the unit has no `[Install]` section, and enabling it
takes tty1 from the login prompt, which is the image's decision to make. The PAM service
file has not been verified against a real stack yet: the layer's tests read its directives
and their order, and its behavior on a host is for a container job on a hosted Linux runner.

## Privileged helpers

Every privileged act leaves the consoles through a closed helper (`layer/helpers`). Each
helper has a socket unit with `Accept=yes` at `/run/olivares-helpers/<name>.sock`, root's and
mode 0600, which starts one helper instance per connection with the connection as its
standard input and output. A helper:

- reads one JSON document of at most 4096 bytes, and refuses a field its schema does not
  name, a null, a repeated key and a second document; it takes no argument but `--help`, and
  no path. Every mutating document carries the `operation_id` its client minted when the
  operator confirmed (128 random bits, 32 lowercase hexadecimal digits), which the helper logs;
  it is not the `request_id` a consumer of an act authorization keeps for itself;
- admits its invoker from the connection alone: the account of the peer's credentials
  (`SO_PEERCRED`), the process the peer's pidfd names (`SO_PEERPIDFD`, Linux 6.5 and later),
  that process's unit, read from its cgroup, and its controlling terminal, both read while the
  pidfd shows it alive. Without a pidfd it refuses every mutating subcommand. A document, an
  argument or the environment never names the invoker. Invokers come from one closed table:
  the repair console on tty1 (root, `olivares-repair-console.service`, `/dev/tty1`), the
  Appliance Console (`olivares-portal`, `olivares-portal.service`) and the network guard
  (`olivares-net-guard`, `olivares-net-guard.service`), which runs as its own static account
  and may invoke the network restore helper alone;
- performs no mutating subcommand yet, whoever asks: per-connection instances wait until the
  appliance image proves, on its own kernel in a guest, that each instance receives its peer's
  pidfd and that a recycled pid is refused. Until then an admitted mutation is refused with
  `pidfd_unproven`, and subcommands that change nothing are unaffected;
- names every file of its spool from a nonce it issued under its own key;
- exits 0 when it performed or answered, 1 when it refused, with no effect, and 2 on a usage,
  setup or input/output failure, with no effect.

| Helper | Runs as | Subcommands | Admitted invokers |
| --- | --- | --- | --- |
| `power` (`olivares-portal-power`) | root (`User=root`), with no capability | `reboot` and `shutdown`: `systemctl reboot` or `systemctl poweroff`, a fixed argument vector, no shell | the repair console on tty1 |
| `support-bundle` (`olivares-portal-support-bundle`) | its static, non-login account (`User=olivares-support-bundle`), whose state directory `/var/lib/olivares-support-bundle` (`StateDirectory=olivares-support-bundle`, 0700) holds its key and its `acts/` and `out/` directories, each 0700; each bundle goes to `out/` | `produce` and `fetch` | none yet: the verb stays unavailable on every surface |

Each helper's service template names its account with `User=`, and none allocates a dynamic
account. Every helper program is installed directly in `/usr/libexec/olivares/`, beside the
consoles. The support-bundle account is static and non-login, with no supplementary group; the
portal package declares it before the helper's unit can start.

Only with a qualified sign-in does the repair console's `power` verb ask the power helper for
`reboot` or `shutdown`, with an operation id it mints for that request, and state what the
helper answered: `performed` means the service manager accepted the request, not that the host
has gone down, and until the guest proof above the answer is the refusal `pidfd_unproven`.
Neither the pidfd proof nor access to the machine replaces the sign-in. The console's
`certificate` and `repair-portal-pam` verbs stay not implemented; each arrives with its own
helper. The consoles reach a helper through `layer/portal/helperclient`,
only on a socket that is root's and not a link: an absent helper is `503
consumer_unavailable` and asks nothing, and a request whose answer was lost is
`outcome_unknown`, never performed. `layer/helpers/polkit/50-olivares-helpers.rules` refuses
the console's account and the support-bundle helper's account every action of the service,
login, disk and package managers, so no act leaves through polkit. `layer/portal/audit` is the
console's append-only audit spool: records wait there while the product's ledger cannot take
them and are ingested once.

No package installs the helpers, their units or the polkit rule yet, and nothing enables the
sockets: that is the image's decision, with the repair console's. A real reboot and shutdown
are verified on an appliance image, not in a container.

### Optional network fields

Existing `host.network: {"mode":"dhcp"}` documents retain the same plan. Optional
`dns.servers` contains literal unicast addresses, and `dns.search` contains DNS
search suffixes. An empty array requests an empty set; omitted fields remain absent.

Static settings use `"mode":"static"` and `interfaces`, for example:

```json
{"mode":"static","interfaces":[{"name":"ens4","ipv4":{"addresses":["192.0.2.10/24"],"gateway":"192.0.2.1"},"ipv6":{"addresses":["2001:db8::10/64"]}}],"dns":{"servers":["192.0.2.53"],"search":["example.test"]}}
```

Each interface has one or both address families, with explicit CIDR addresses and
an optional gateway within an address prefix. Duplicate names or addresses, wrong
families, loopback, unspecified and mapped addresses are refused. First boot verifies
these settings against cloud-init's NetworkManager output. A local answers file or
systemd credential alone cannot configure static networking: also supply matching
cloud-init network configuration, such as a NoCloud seed's `network-config`.

## The image recipe

`appliance/images` is the recipe of an appliance image: ONE KIWI NG description with a
profile per edition and architecture (`kiwi/config.xml`, profile `fedora44-server-amd64`,
the shipping base, built from the signed product RPMs; `debian13-server-amd64` stays buildable
with `--base debian13`), the assembly of the formats built from the disk it produces (`formats/`), and the
tests that hold both to their shape. `appliance/test` is the boot side of the same recipe.

```sh
# Hosted current job only, after staging the Community and layer packages:
task appliance:build ACCELERATOR=kvm EVIDENCE="$RUNNER_TEMP/appliance-attempt" FORMAT=all
# Standalone guest observation over existing artifacts (no builder admission):
task appliance:boot ACCELERATOR=kvm
# TCG is an explicit choice and must match the prerequisite trial:
task appliance:build ACCELERATOR=tcg EVIDENCE="$RUNNER_TEMP/appliance-tcg" FIRMWARE=free
```

Neither target is a push gate and neither is on the hook's fast path: an image build needs a
container runtime and privileges a push must never take. The hosted run is
`.github/workflows/appliance-image.yml`, dispatched by hand; it builds the three artifacts,
boots them and publishes nothing. All phases share that job; no downloaded receipt from another
VM can admit a builder. The recipe has no separate Containerfile or KIWI installation.
`FORMAT=all` is the single qualification transaction: iso, qcow2 and ova are built together
because the guest cases need the disk and the installer. Any other `FORMAT` is refused:
`task -x appliance:build FORMAT=qcow2` exits 2, while plain `task` reports its own status 201
for any failing command.
The attempt runs `formats/assemble.sh --format` once per format as one of its steps; calling it
alone is not a supported or qualified export.
The owner always attempts release after failure; hard runner loss or an unknown daemon
outcome remains unconfirmed cleanup. Evidence and recipe work are retained, never erased
as proof of cleanup. See [builder admission](images/toolchain/README.md).

The build, format, boot and NoCloud phases stream their output into `<phase>.log` as it
arrives, so an interrupted phase keeps what it printed. Such a log keeps its first 16 MiB and
its last 256 KiB, and `<phase>.log.json` records the raw exit, start, finish, duration and any
interruption. The preflight is the exception: it runs through the prerequisite's unchanged
`workflow.py` and collector, which keep the last 256 KiB of output and write
`prerequisite/preflight.log` only when the collector ends. There is no `preflight.log.json`.
An interrupted preflight's log holds only the line "collector interrupted; resource release
unconfirmed", and a receipt the collector never wrote is replaced by the explicit incomplete
receipt. `prerequisite/workflow-result.json` records the preflight's exit, and
`recipe-result.json` lists every phase's times, the preflight's included. `disk-samples.jsonl`
samples free bytes of `/`, the evidence and the target directory at each phase start and
every 30 s. Use between samples is not observed, so the
largest sampled drop is a lower bound of peak use, not the peak. The Actions log receives one
progress line per 30 s: phase, elapsed time, output bytes, free bytes and the last output
line, at most 160 printable characters. The job reads no secret. The evidence artifact keeps
text records, guest `seconds`, `qemu.cmd` and `qemu.stderr`, but no guest disk. Only the
streamed phase logs are byte-capped. The guests' `serial.log`, NoCloud's `console.log` and
`probe.log`, and each `qemu.stderr` are written by QEMU or a shell redirection with no byte cap.
They are bounded only by each guest's timeout (900 s boot, 1800 s install, 1200 s NoCloud) and
are retained whole. No total byte budget is declared for the evidence artifact.

Release records are never rewritten. The first release writes `cleanup.json`, `release.log`
and the prerequisite's `prerequisite/builder-release.json`. Each later release, such as the
always step or a retry after a failed release, claims a new `recovery/NNN/`. It holds its own
receipt copy, `builder-release.json`, release log, `cleanup.json` and `SHA256SUMS`. A record's
`SHA256SUMS` also hashes the manifest before it, so the latest sealed record commits to every
earlier one. A record without `SHA256SUMS` was interrupted and stays as it was left. The
attempt's own `SHA256SUMS` is written once, by the attempt or, if the attempt was stopped
first, by the first always step. `cd EVIDENCE && sha256sum -c SHA256SUMS recovery/*/SHA256SUMS`
verifies them all. A cidfile that names another owner's container, and a labeled container
with no cidfile, are refused and nothing is deleted.

The attempt exits 0 when every phase and the release completed, and 2 only when the
prerequisite or a phase classified a missing prerequisite. Any other failure is 1, including
a timeout (124), a missing command (127) and an interruption; `recipe-result.json` keeps the
raw `phase_exit`. A builder command that fails after admission makes `build.sh` exit 1 whatever
its container returned (GNU tar's fatal status is 2); `build.log` keeps that raw status.
Release follows the prerequisite receipt's typed custody. A builder the collector already
removed, even without a recorded ID, or never retained, is not released
again; the collector's own `cleaned` result decides whether that custody is confirmed.

| Piece | What it is |
| --- | --- |
| `kiwi/config.xml` | the one description: profiles, disk layout, package lists, repositories |
| `kiwi/config.sh` | what runs inside the image root: the serial console, the datasources, and a template with no instance identity, checked by the layer's own `appliance-firstboot check-template` |
| `toolchain/input-lock.json` and `toolchain/Containerfile` | the single reviewed builder owner; exact Debian snapshot, source and artifact closure |
| `kiwi/attempt.py` | one same-job preflight/admission/build/boot owner; exact child and image retirement in finally, with an always-step recovery path |
| `kiwi/build.sh` | rechecks current admission before effects, uses the immutable image ID, stages the dracut modules from the builder's lock-declared path, and records attempt/image/accelerator/input digests |
| `kiwi/product-dev.nfpm.yaml` | a DEVELOPMENT product package so an image can be built before the release chain learns the image artifact type (A3). The published package is the release chain's |
| `formats/` | `iso.sh`, `qcow2.sh`, `ova.sh` and the declaration `formats.json` they share, each writing the artifact's manifest beside it |

The image installs the product and the appliance layer as **packages** and starts nothing.
Every identity — host keys, machine ID, TLS, setup token, signing keys — is made on the first
boot of each instance, and `config.sh` fails the build if the finished root already carries
one. The description takes packages from Debian 13's mirrors and from the appliance's own
local repository, and from nowhere else; KIWI's dracut modules, which Debian does not package,
come from the same pinned KIWI release the build uses.

`FIRMWARE=nonfree` (the default) adds redistributable non-free firmware, which is what the
official Debian installer has carried since bookworm and what makes the image boot on most
server network and storage controllers. Whether the published image carries it is still the
owner's decision; `FIRMWARE=free` answers it the other way and the image is then 100% free.

### Sizes, and the ceiling

While distribution is GitHub only, no file of a release may pass **2 GiB**. Each artifact
declares its own ceiling in `formats.json` and the assembly refuses to hand over one that
passes it, so an image that grew is a build failure and not a discovery at upload time. The
server edition is predicted at roughly 1.2–1.5 GiB per artifact and has never been measured:
the first hosted build replaces the prediction with the weight. An edition that cannot fit —
the workstation edition with a full desktop is the candidate — declares itself split in the
same file, with the size of its parts.

### What the recipe does not claim

No artifact of this phase is signed and none is published: checksums, signatures, SBOM, VEX,
provenance and the GPL source offer are the release phase's (A3), and the recipe says so in
every manifest it writes (`"signed": false`). The OVA is well formed and its manifest matches
its contents; whether vSphere, VirtualBox and Proxmox import it is the owner's laboratory row,
stated and not run. The disk does not grow to its target: KIWI's resize capability is a dracut
module Debian does not package, so the disk is the size the description declares.

## The boot battery

`appliance/test/boot-battery.sh ARTIFACTS EVIDENCE` is the acceptance oracle: an appliance
that does not reach `multi-user.target` and show the console label on its serial console has
not booted. It answers that on amd64 four times:

| Case | What it boots | What it proves |
| --- | --- | --- |
| `bios` | the disk under SeaBIOS | the legacy path KIWI keeps on the same disk |
| `uefi` | the disk under OVMF | shim and the signed GRUB under EFI |
| `uefi-secureboot` | the disk under OVMF with the Microsoft keys enrolled | the firmware verified the chain; the kernel says it was booted with Secure Boot on. Removing `shim-signed` from the recipe makes this case red and no other |
| `iso-install` | the installer medium, unattended, then the disk it wrote | the medium an owner burns installs and boots |

It **refuses rather than passing**: an explicit KVM choice without `/dev/kvm` exits 2 before
QEMU. An admitted TCG choice is passed unchanged to every guest consumer; unknown choices
refuse. The existing 900s boot, 1800s install and 1200s NoCloud ceilings are declared bounds,
not measured TCG performance. A prerequisite prelaunch proof does not establish guest support
or a usable boot budget. Full guest observations remain required on the composed head.

`appliance/test/nocloud-probe.sh` is the unattended first boot on a real machine: it boots the
disk from the seed the layer's own carrier fixture delivers and asserts the first-boot record
with that battery's own assertions, loaded rather than copied. The appliance ships no
credential, so there is no shell in the guest to read a file from: the probe hands systemd an
observer unit through an SMBIOS credential, and the unit prints the record to a second serial
port and powers the machine off. The image is not modified.

`appliance/test/battery-selftest.sh` is what both promise before they boot anything — the
three firmware cases are named, the refusals refuse, the label is the layer's — and it runs on
any machine, with no hypervisor and no artifact.
