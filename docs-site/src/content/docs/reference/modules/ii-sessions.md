---
title: "Module II — live operation & sessions"
description: >-
  The live operational overlay per agent session: current action, live
  tokens/cost, a derived state and a replayable timeline, plus the managed
  runs the plane launches under a provider profile. What it derives, what
  stays honestly empty, and the limits.
---

Module II is the **live operation** view of the estate: what every agent session
is doing right now, its live token and cost totals, a derived liveness state,
and a reconstructable timeline. Where module I (inventory) materializes the durable
estate, module II keeps a **live operational overlay** per session over the same
observation stream — and shows only what that stream honestly carries.

Olivares <!-- release -->0.9<!-- /release --> also **launches** official provider CLIs as owned children under a
[provider profile](/how-to/operate-provider-sessions/). That managed path is
the same module. It does not replace the overlay, and it does not merge two
homes that announce the same provider session id (`CHANGELOG.md` `[26.9.0]` B1/B2).

## What it is

Module II is a bus-driven Core-layer module, sibling to inventory. It maintains a
live record keyed by each session's external reference, built from the cooperative
observation stream — never polled, never fabricated. Per session it tracks:

- the **current action** (the last tool used) and the resource/mode it touched;
- the **live token and cost totals**, read from cost samples (the canonical cost
  ledger and FinOps are module XI, not here — this is the live figure only);
- a **derived liveness state** (`cc_state`, still named for the original Claude
  Code derivation); and
- a **timeline** to which every observed event is appended in ingest order.

## Its contract & entities

The module registers two tenant-scoped entities. `sessions.live` holds the live
record per session — current action/resource/mode, model reference, live
input/output tokens, live cost, event and tool-call counts, and first/last
event timestamps. `sessions.timeline` holds one replayable row per event, ordered
by ingest. There is **no stored lifecycle column**: the cooperative stream carries
no end-or-fail signal, so the only honest liveness signal is the derived `cc_state`.

`cc_state` is derived **at read time** from event recency — `active` / `idle` /
`ended` — and flips to a silent-evasion state when the connector raises that
finding (it is never written by the module itself). Reads are served under module
routes (live list, single session, per-session timeline) plus a live SSE stream;
every read requires the session read permission, and **opening the stream is
auto-audited**. The SSE channel is strictly **tenant-isolated** (a client receives
only snapshots for its authorized tenant) and **best-effort** (a slow client drops
the intermediate frame and gets the next — ingest never blocks).

## Provider profiles and `live_ref`

A **provider profile** is the durable identity of one configured provider
instance on one execution environment: driver, environment, and the canonical
`config_home` / `user_home`. It is not an authenticated provider account.
Register, rename, disable/enable and retire live under
`/v1/m/sessions/provider-profiles`. Paths appear only on the admin
`configuration` read. A launch names `provider_profile_ref`; the server
resolves the homes and persists a non-secret snapshot on the run before spawn
(`CHANGELOG.md` `[26.9.0]` B1; `web/src/features/agentops/types.ts`).

An observation folds into the live row of its **channel**, computed by the
server from the host-stamped source registration (`CHANGELOG.md` `[26.9.0]` B2):

| Channel | Meaning |
|---|---|
| `legacy` | no registration |
| `observed` | a source dedicated to a profile by a binding approved at host admission for the exact applied revision |
| `source` | a known registration with no verifiable profile |
| `managed` | a run the plane launched; the only row carrying `canonical_sid` and `run_ref` |

Every live row exposes `live_ref` and `attribution`. Two homes that announce
the same provider session id are two rows with two timelines. Read one row
with `GET /v1/m/sessions/live/by-id/{live_ref}` (and its timeline / stream /
runs query). Bare external-id routes stay and are **legacy**: they answer for
the legacy row only.

Drivers are registered on each node at boot. At launch, the engine uses the
newest verified managed install, then the CLI on the engine's `PATH`.
`OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`, `_CODEX_BIN`, `_GROK_BIN` and `_OPENCODE_BIN`
explicitly override this resolution (see [Configuration](/reference/configuration/)).
If no executable resolves, launch is refused. Other readiness checks, including
profile authentication and launch policy, still apply. Operator steps:
[Operate a provider session](/how-to/operate-provider-sessions/).

## Provider account usage

`GET /v1/m/sessions/provider-accounts/{ref}` and the account list carry the
latest `usage[]` facts reported by that account's own tool home. The CLI exposes
the same response through `olivares provider account get <ref> -o json`.
These readings come from structured tool responses, not session text.

A percentage row has `window`, `label`, `utilization` (percent used),
`observed_at`, and `resets_at` when reported. Window names are open-ended:
`five_hour`, `seven_day`, per-model windows, and Claude Code's `monthly_spend`
are examples. Extra-usage utilization appears as `monthly_extra_usage` when
the tool reports no spend window. A missing reading is unknown; a reported
zero remains zero.

Codex credits use a separate kind of row, without a percentage:

```json
{
  "window": "credits",
  "label": "Credits",
  "credit": { "balance": "12.50", "has_credits": true, "unlimited": false },
  "observed_at": "2026-10-29T10:00:00Z"
}
```

`balance` preserves the tool's decimal string. It is omitted when unknown;
`"0"` is a known zero. `unlimited` and `has_credits` are the tool's flags,
not percentages. A zero credit balance does not mean a percentage window
has reached its limit. Existing percentage rows keep their fields.

## Managed-run lifecycle and restart boundaries

Sessions is a kernel module: it keeps running even when it is not in the saved
module selection. Turning off an optional module does not reset session data.

The managed-run API under `/v1/m/sessions/runs` and `olivares session` use the
same runtime. Listing and metadata require `sessions:run:read`; output requires
`sessions:output:read`, held by editors and above by default. Launch, input and
resume require `sessions:run:write`; stop requires `sessions:stop:write`. A viewer
keeps listing and metadata but loses session content without an explicit output
grant. Tenant and workspace authority still apply to each run.

```sh
olivares session ls -o json
olivares session show <session> -o json
olivares session send <session> "continue the task"
olivares session stop <session>
olivares session resume <session>
```

A message can carry images. `olivares session send <session> --attach shot.png "what
is wrong here?"` and the console composer (paste, drop or the paperclip) store each
PNG, JPEG, WebP or GIF with the session, up to four per message and 3.75 MB each, then
send them with the text; a message needs its text, so an image alone is not sent
(the CLI and the composer ask for it). Claude Code receives an image content block and Codex its own
image input; a tool with no image input refuses the message by name before anything is
sent. The API is `POST /v1/m/sessions/runs/{ref}/attachments` (the image, base64),
`GET .../attachments/{id}` and `attachments` on `.../input`. Images live under the data
directory, per tenant and session, and are removed with the session.

Stopped runs, their provider-profile and workspace references, and lifecycle
events survive an engine restart. Resume starts a new owned child for the
existing run. Live attach output is an in-memory bounded ring; retained run
metadata and lifecycle events do not promise a persisted conversation transcript.
Recording policy and the tool's own resume support remain separate requirements.

## Session filesystem confinement

Managed sessions and local stdio MCP servers require Linux Landlock. If the
engine host cannot provide it, the launch is refused before the tool starts;
no permission preset opts out. Enable Landlock on the engine host and run
`olivares doctor` there to check availability. A doctor check on another machine
only describes that machine, not the remote engine.

Confined children receive access to their session folder, profile homes and a
private temporary directory. The engine database, secret-store key and engine
configuration stay outside those grants. This filesystem boundary does not
provide network isolation or container isolation.

A session's folder is writable, and the Git metadata inside it would otherwise be
too: a hook or a `core.hooksPath` the session wrote runs the next time you use `git`
there. Landlock cannot deny a path inside a directory it grants, so the session (and
a local MCP server started in the folder) runs in a mount namespace of its own in
which the repository's `hooks` directory and `config` file, `config.worktree`, and in
a linked worktree the files that name the common directory, are read-only; the Git
directory cannot be moved away. Git reads `commondir` and `config.worktree` from a Git
directory when they exist, so the launch creates them where they are missing (a
`commondir` that names the directory itself, an empty `config.worktree`; both mean
nothing to git) and binds them read-only, so the session cannot create its own. `git add`, `git commit` and new files work as
before. `git config` in the folder is refused: a session with no Git identity
commits with `git -c user.name=... -c user.email=... commit` or the `GIT_AUTHOR_*`
and `GIT_COMMITTER_*` variables. A branch made from a remote-tracking branch is
created without its upstream (git reports that it could not write the
configuration file).

The protection of a file lasts as long as the file. It is a mount on the file in the
session's namespace, and when something outside the session deletes the file or
renames another over it, the mount is gone there and the running session can write the
new file. Git replaces `config` that way for `git config`, `git remote add`,
`git push -u`, `git branch -u`, `git checkout -b <branch> <remote>/<branch>` and
`git sparse-checkout`, so run them before a session starts, or start a new session
after them. An empty `hooks` directory that is removed is the
same case; one with files in it cannot be replaced. This does not cover other
ways a repository can run code, such as a `core.hooksPath` that points into the
folder, a sibling worktree's entry, a submodule's Git directory, a repository nested
in the folder, a folder that has no `.git` yet, or the same files reachable through a
second mount point.

This needs unprivileged user and mount namespaces. Where the host lacks them, the
session still starts and its confinement line says that the Git hooks and
configuration stay writable. The packaged systemd units and the Compose profiles allow
the mount namespace and the `mount` call this needs. A host that has the
namespaces but cannot bind one of the files at launch (a path removed in between, an
unusual filesystem) does not start the session: a protection the host should give
and does not is a failure, not a limit.

## Session folders on NFS or SMB shares

A session folder is a path on the engine host, so a network share is a folder once the host has
mounted it. `olivares netfolder add` does that with systemd's own mount units and registers the mount
point as a folder; the mounting stays systemd's, and the units are plain files you can read and edit.
Run it as root on the engine host:

```sh
printf '%s\n' "$SMB_PASSWORD" | sudo olivares netfolder add files //fileserver/projects --user svc --password-stdin
sudo olivares netfolder add data fileserver:/export --mode write_no_delete
olivares netfolder ls
```

The source is `//server/share` for SMB or `server:/export` for NFS, as `mount(8)` writes them. The name is
lower-case letters and digits, and names the mount point `/srv/olivares/mnt/<name>`. `--mode` is the folder's
access mode (`rw` by default). The units name the account `olivares` as the owner of every file an SMB
folder shows, so the engine must run as that account. `ls` shows each folder as `mounted`, `unmounted` (it mounts on first use, and
`ls` does not trigger that) or `not responding`. A folder that exists is never overwritten.

For the SMB example the command writes these units, measured on Debian 13 (Linux 6.12) against an NFSv4.2
export and a Samba 4.22 share (SMB 3.1.1). The file names are what
`systemd-escape -p /srv/olivares/mnt/files` prints with `.mount` and `.automount` added.

```ini
# /etc/systemd/system/srv-olivares-mnt-files.mount
[Unit]
Description=Olivares network folder files
After=network-online.target
Wants=network-online.target

[Mount]
What=//fileserver/projects
Where=/srv/olivares/mnt/files
Type=cifs
Options=credentials=/etc/olivares/netfolders/files.cred,uid=olivares,gid=olivares,vers=3.1.1,nodev,nosuid,soft,echo_interval=5
TimeoutSec=30
```

```ini
# /etc/systemd/system/srv-olivares-mnt-files.automount
[Unit]
Description=Olivares network folder files (mounted on first use)

[Automount]
Where=/srv/olivares/mnt/files
TimeoutIdleSec=0

[Install]
WantedBy=remote-fs.target
```

For NFS it writes `Type=nfs4`, `What=fileserver:/export` and
`Options=nodev,nosuid,vers=4.2,soft,timeo=50,retrans=2`, and no credential file. On a host that enforces
SELinux with the appliance's policy it adds `context=system_u:object_r:olivares_workspace_t:s0` to the options,
the label the engine and its session tools already use for a folder. To remove a folder, run
`systemctl disable --now` on its two units, delete them and its credential file, and deregister the folder.

- **Everyone who has the folder acts as one account.** Files are read and written as the account the
  mount presents: `uid=` and `gid=` for SMB, the export's user mapping for NFS. The share does not tell
  people apart. Olivares decides who receives the folder.
- **A person's own folder reads as that person.** `--owner <user id>` registers an SMB folder as one
  person's own, as a personal folder is: its content is theirs alone, and administrators still find the
  folder to manage it. Give `--user` and the password of that person's own SMB login and the file server
  applies their rights, so two people mounting the same share each see what their own account may open.
  Add one folder per person, each with its own name. NFS cannot tell people apart and takes no owner.
  The administrator types the person's password once, and it stays in the root-only credential file like
  the shared account's; the person's session never holds it. A per-session Kerberos ticket, so that the
  person need not hand the password to anyone, is not built. Checked with two accounts of a Samba AD
  domain on one share: each person's folder listed their own directory and the common one, read their
  own file, and the file server refused the other person's file.
- **The credential stays in a file.** The command writes `username=` and `password=` to the `credentials=` file,
  owned by root with mode 0600, from the password on standard input. The password appears in no unit, mount option, process argument or
  journal line: `mount.cifs` receives the path only. NFS with `sec=sys` needs no credential.
- **The automount unit keeps boot independent of the file server.** Nothing waits for the share at
  boot. The first use mounts it, and a server that is down at that moment fails the use, not the boot.
- **Use `soft`.** A `hard` NFS mount, the default, leaves a process that touches a dead server stuck
  where it cannot be stopped. With the options above, a read or write on a mounted share fails with an
  input/output error or "Host is down" after 20 to 30 seconds, and the share works again once the server
  answers.
- **A folder that does not respond is reported.** Registering the folder, opening it, listing it and
  starting a session in it each wait 10 seconds for the share, then refuse with
  "The folder ... is not responding. If it is on a network share, check that its file server is up and
  reachable." and a 424 status. The session does not start.
- **`write_no_delete` holds.** Landlock enforces it in the kernel's file system layer, above the
  network file system. In a real session launch on both shares, a session created and changed files and
  was refused rename, unlink and rmdir, exactly as on a local disk, and `rw` allowed them. A host that
  cannot confine still refuses the launch.
- **The folder is the boundary.** In the same launches, in both modes, a write to the folder's parent,
  which is on the same share, was refused and left no file. The session writes inside its folder only.

## What it consumes (and what it derives)

Module II consumes the same minimal-data observation stream as inventory —
[`edge.observed`](/reference/events/), `cost.sampled` and `finding.reported`.
Only edges whose origin is a **session** produce live operation; cost samples tied
to a session add to the live token/cost figure (no `CostRecord` is written here);
session-subject findings are annotated, and an anti-evasion finding marks the
evasion state. Two fields are **derived live** from those same signals: `agent_ref`
from a session's attributed agent, and `summary` from a context-compaction
(forensic) finding whose title is summary-safe by contract — never an LLM-fabricated
summary.

:::caution[Honest limits]

- **`goal` stays empty — honestly.** The cooperative stream is minimal-data and
  does **not** carry a session's goal or task list; they are redacted at the
  connector and there is no in-process prompt text on the wire. The live record
  models the field so the contract and UI are ready and any future metadata channel
  can populate it, but the module **never invents it**.
- **No stored lifecycle.** The stream has no end/fail signal, so a session's
  liveness is the **derived** `cc_state` by recency — not a persisted status. An
  `ended` state means *no recent events*, not a confirmed clean shutdown.
- **The live figure is not the ledger.** Live tokens/cost are an operational
  reading from cost samples; the authoritative, reconcilable cost record is module
  XI's FinOps ledger. Do not treat the live figure as billing truth.
- **Minimal-data is a property of the wire.** Only references, classifications and
  liveness/cost counters are carried and persisted — never payloads, prompts,
  commands or PII.
:::

## Related

- [Event bus reference](/reference/events/) — the `edge.observed`, `cost.sampled`
  and `finding.reported` events this module consumes.
- [Modules catalog](/reference/modules/overview/) — where module II sits and the
  honest actuate split.
- [Access & resource map](/reference/modules/iii-access-map/) — the sibling Core
  module that owns the R/RW access graph.
- [Architecture overview](/explanation/architecture/overview/) — the engine and layers.
- [Connect Claude Code](/how-to/connect-claude-code/) — start producing the cooperative live stream.
- [Operate a provider session](/how-to/operate-provider-sessions/) — register a profile and launch Codex, Grok or Claude.
- [Honesty & limits](/start/honesty-and-limits/) — what the product does and does not do today.
