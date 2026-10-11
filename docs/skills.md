# Assigned skills in sessions

Enable the Skills module with `olivares modules on skills`, install a reviewed
pack, and assign its revision with `olivares skills assign`. Workspace, template,
agent, agent group and session assignments deliver the selected `SKILL.md` files
and their supporting files when a native Claude Code or Codex session starts.
Installing a pack alone delivers nothing.

Claude Code discovers them at `$HOME/.claude/skills/<name>/SKILL.md` through the
session's `olivares` plugin; its skill names are `olivares:<name>`. Codex discovers
them at `$HOME/.agents/skills/<name>/SKILL.md`. These locations are in a temporary
HOME managed by the engine. The account's configuration home remains in use.
The engine never places assigned skills in the session's repository.

The delivered tree and its discovery directory are read-only. Cache directories
remain writable. Assigned delivery requires a native session and filesystem
confinement with truncation protection; unsupported hosts or launch forms refuse
the launch with an explicit error. Sessions with no assignments keep their
ordinary HOME and launch behavior. Turning the Skills module off disables delivery.

Inherited assignments are pinned for the conversation and retained on resume.
New session-specific assignments join that snapshot on the next launch. Conflicting
content for the same skill name refuses delivery. Removing an assignment changes
future inheritance; recorded conversations retain their pins and prevent retirement
of the referenced pack.

Before spawning, the engine verifies the revision's original manifest and file
hashes, then records one `sessions.skills.delivered` audit event for the launch,
with the pack and revision IDs, manifest and member hashes, and selection SHA-256.
It removes the temporary skill HOME when that launch ends.
