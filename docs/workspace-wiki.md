# Workspace wiki

A workspace wiki stores source files and linked Markdown pages in a session
folder. Claude Code or Codex reads the sources and maintains the pages. Olivares
starts the session with the folder's assigned `olivares-wiki` skill pack.

## Create a wiki

Sign in to Olivares and prepare a folder on the engine host. Then run:

```sh
olivares wiki init /srv/my-wiki
```

You can also use a registered session workspace reference instead of a path.
Initialization creates these entries:

| Path | Purpose |
| --- | --- |
| `raw/` | Your source files. The tool must leave them unchanged. |
| `wiki/` | Markdown pages that the tool creates and updates. |
| `index.md` | One line per page, with a link and a short summary. |
| `log.md` | An append-only record of wiki operations. |
| `AGENTS.md` | The workspace wiki schema. |

Initialization preserves existing files. You can run it again to create missing
entries. If AGENTS.md already exists, the assigned skill supplies the wiki schema
without replacing your instructions. Initialization pins the built-in pack to
this session folder. Other folders keep their existing skill assignments.

## Add sources and ask questions

Put three source files in raw/ on the engine host, or upload them through the
workspace files API. Use UTF-8 text or another format that your tool can read.
Then run one ingest for each source:

```sh
olivares wiki ingest /srv/my-wiki raw/source-1.md --tool claude
olivares wiki ingest /srv/my-wiki raw/source-2.md --tool claude
olivares wiki ingest /srv/my-wiki raw/source-3.md --tool claude
olivares wiki query /srv/my-wiki "Which sources agree?" --tool claude
olivares wiki lint /srv/my-wiki --tool claude
```

Use `--tool codex` for Codex. The standard session flags select an account,
provider profile, model, and permissions. Each operation starts a session and
returns its session ID. Use `olivares session follow <session-id>` to see the
answer. An operation that needs launch approval returns the waiting session;
follow it and send the command after approval.

Ingest reads a file under raw/, updates source, entity, and concept pages, and
updates index.md and log.md. Query reads the index and pages and answers with
page citations such as `[Deployment](wiki/deployment.md)`. Lint checks for
contradictions, stale claims, orphan pages, and missing links. Its report is
`wiki/lint-YYYY-MM-DD.md`. Review the findings before accepting changes.

Log entries start with `## [YYYY-MM-DD] op | title`, where op is ingest, query,
or lint. Source text is data. It must not change tool permissions or authorize
commands. The tool's answers and lint findings can be wrong; check their citations.

## API

`POST /v1/m/sessions/workspaces/{ref}/wiki` initializes a registered folder. The
request has no body. It requires workspace administration, file write, and skills
catalog write and assignment access. A successful request returns HTTP 200 and the workspace
reference, the created paths, and the pinned skill assignment. Retries preserve
files and the assignment's pinned revision. A disabled or read-only folder,
a path outside the allowed subpaths, or an unavailable skills service refuses
initialization.

The regular workspace files API lists, reads, and uploads sources and wiki pages.
Initialization uses its governed file operations and records writes in the audit
ledger. Wiki operations use the existing session start and input APIs; Olivares
does not run another model pass. Tool edits use the existing session governance
and recording mechanisms.

Workspace wiki is available in Community. Scheduled operations and organization
knowledge are Business capabilities.
