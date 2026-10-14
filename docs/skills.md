# Assigned skills in sessions

Skills is on by default on fresh installations and remains optional. Upgrades
preserve the saved module selection; if Skills is off, enable it with
`olivares modules on skills`. Install a reviewed pack and assign its revision
with `olivares skills assign`. Workspace, registered folder, template,
agent, agent group and session assignments deliver the selected `SKILL.md` files
and their supporting files when a native Claude Code, Codex, Gemini CLI, Grok
Build or OpenCode session starts.
Installing a pack alone delivers nothing.

Use `--folder <workspace-ref>` to assign to one registered session folder.
`--workspace` assigns to the organizational workspace.
[Workspace wiki](workspace-wiki.md) initializes a folder and pins its bundled skill.

Claude Code discovers them at `$HOME/.claude/skills/<name>/SKILL.md` through the
session's `olivares` plugin; its skill names are `olivares:<name>`. Codex, Gemini
CLI, Grok Build and OpenCode discover them at
`$HOME/.agents/skills/<name>/SKILL.md`. These locations are in a temporary
HOME managed by the engine. The account's configuration home remains in use.
For Gemini CLI, `GEMINI_CLI_HOME` also names this temporary home, whose `.gemini`
links to the selected profile's configuration directory.
The engine never places assigned skills in the session's repository.

The `.agents/skills` alias is documented by [Gemini CLI](https://geminicli.com/docs/cli/skills/),
[Grok Build](https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/08-skills.md)
and [OpenCode](https://opencode.ai/docs/skills/). A tool without a documented native
skills folder refuses assigned delivery with its name and the reason.

The delivered tree and its discovery directory are read-only. Cache directories
remain writable. Assigned delivery requires a native session and filesystem
confinement with truncation protection; unsupported hosts or launch forms refuse
the launch with an explicit error. Sessions with no assignments keep their
ordinary HOME and launch behavior. Turning the Skills module off disables delivery.

Inherited assignments are pinned for the conversation and retained on resume.
New session-specific assignments join that snapshot on the next launch. Removing an
assignment changes future inheritance; recorded conversations retain their pins and
prevent retirement of the referenced pack.

## Precedence

A skill name has one owner in a launch. Targets are ordered from the broadest to the
narrowest: workspace, folder, template, agent group, agent, session. When two targets pin
different content under one skill name, the narrower target's skill replaces the
broader one's, so a project overrides a generic pack by installing its own skill of that
name and pinning it to its folder, without editing the generic pack. Two targets of the
same kind (two agent groups, or two packs on one target) that disagree still refuse
delivery with `skill_name_conflict`. A resumed conversation keeps a narrower pin over a
broader target that appears later.

## Skills that act when loaded

A native tool acts on some content the moment a skill loads, outside the session's own
approvals: `hooks`, a `!` shell interpolation in the body, an `allowed-tools`
pre-approval, and a plugin, settings or hooks file. A revision lists these per skill as
`active_content`, and `olivares skills diff` and the update check name what an update
newly adds. Pinning re-reads the revision's own bytes under the current rules, so a
revision published before a rule existed is held to it too. The diff and the update
check return file text and need `skills:catalog:write`; reading the catalog does not. Pinning such a skill needs `approve_active_content` on the assignment
(`--approve-active-content` on the CLI, a checkbox in the console); the audit event of
the pin records that it was approved. Members an existing pin already holds at the same
revision are not asked again, so narrowing a pin never needs a second approval. A
`scripts/` folder is not listed: scripts run only when the model runs them through the
session's own approvals. Installation and delivery never execute pack content.

Of the built-in ECC packs, two carry such a skill, both through `allowed-tools`:
`videodb` in `ecc-media-generation` and `inherit-legacy-style` in
`ecc-workflow-quality`. Pinning a whole one of those packs needs the approval; pinning
any other skill of them, or any skill of another built-in pack, does not. Pins made
before this rule keep working unchanged.

Before spawning, the engine verifies the revision's original manifest and file
hashes, then records one `sessions.skills.delivered` audit event for the launch,
with the pack and revision IDs, manifest and member hashes, and selection SHA-256.
It removes the temporary skill HOME when that launch ends.

## Install from the console or CLI

Open **Skills** (`/skills`) to browse the built-in catalog. Choose a workspace
and click **Install** to install a pack and pin the returned revision in one action.
**Catalog only** installs without assigning. **From Git** accepts a public HTTPS
repository URL, an optional ref (default `HEAD`) and an optional subdirectory.
Set **Pack name** when installing another pack whose repository name is already
in the catalog.
The console uses the same validation and immutable publication as the CLI.
If workspace assignment fails after installation, **Retry assignment** uses the
published revision. **Retry install** retains the import key after a lost response.

```sh
olivares skills ls --builtin
olivares skills install --builtin ecc-security --idempotency-key security-install-1
olivares skills install --git https://github.com/owner/skills --ref <commit> --subdir skills
olivares skills assign <pack-id> --revision <revision-id> --workspace <workspace-id>
```

Built-in archives are checked against their pinned SHA-256 digest. Git refs are
resolved to a commit, and the validated source, selected subdirectory and manifest
digests are recorded in the revision. An update publishes another revision; existing
assignments keep their pins until explicitly changed. Installation never executes pack
code.
`GET /v1/m/skills/builtins` lists the built-in pins before any pack is installed;
reading this catalog requires `skills:catalog:read`.

## Update, pin and roll back

An update reads the pack's recorded source again: the built-in catalog of this binary,
the same Git URL and subdirectory at the recorded (or a newly named) ref, or a
registered folder. A pack installed from an uploaded archive or a folder snapshot has no
origin to ask; install the new version with `skills update <pack-id> --folder|--archive`.

```sh
olivares skills update <pack-id>                  # current, or available with its diff and digest
olivares skills update <pack-id> --diff           # include the text of every changed file
olivares skills update <pack-id> --apply --expected-digest <digest>
olivares skills diff <pack-id> [--from <rev>] [--to <rev>] [--diff]
olivares skills pin <pack-id> [--revision <rev>] [--dry-run]
```

Applying publishes only the digest that was reviewed, so a source that moved in between
is refused with `source_changed`. `skills update ... --ref v2` moves a Git pack to that
tag, and later updates follow it. The diff compares verified artifacts: members added,
changed or removed, files with their hashes and sizes, and a bounded unified text diff.
`skills pin` moves every pin of a pack that you can write to one revision, keeping each
pin's selected skills; with an older `--revision` it is the rollback. A pin whose skills
are not in the revision is reported and left as it was. The console's pack sheet has the
same check and apply, and **Use latest** on each assignment.

## Enable, disable and preview

```sh
olivares skills enable tdd-workflow code-review --folder <folder-ref>
olivares skills disable deploy --folder <folder-ref>
olivares skills selection --workspace <workspace-id> --folder <folder-ref>
```

`enable` finds the installed pack that holds each skill (`--pack` when two do) and pins
its latest revision, or extends the pin the target already has at its revision.
`disable` removes skills from a target's pins and drops a pin left empty. A skill that
comes from a broader target stays there: disable it on that target, or pin your own
skill of the same name on the narrower one. `selection` resolves the tree you name with
the launch's own rules, so it shows what a new session there gets and from which target.
The same read is `GET /v1/m/skills/selection?target=workspace:<id>&target=folder:<ref>`.

