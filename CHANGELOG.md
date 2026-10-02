# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses [CalVer](https://calver.org/): a monthly release is `YY.M` (two-digit
year and month, such as 26.10), and a patch release adds a third number, `YY.M.N`. Tags have
no `v` prefix; the latest release is tagged `26.10.1`.

> **Status: beta.** The latest published release is **26.10.1**; its dated section below is the
> record of what it shipped. Changes for the next release are recorded under **[Unreleased]**,
> with no release date until it is published. The
> [releases page](https://github.com/olivaresai/olivares/releases) lists every published release.
> Release notes and artifacts become authoritative when the corresponding release is published.
> Every earlier release keeps its own dated section, unchanged.
> Published CLI commands and flags, API routes and fields, configuration keys, defaults,
> paths and stored data stay compatible in later releases, also during beta. A removal is
> first announced as a deprecation in an earlier release and comes with an automatic
> migration. CalVer names the release month; it never permits a regression.
> Because CalVer does not encode breaking changes in the version number,
> every breaking change is called out explicitly under **Changed**/**Removed** here. No
> versions, tags, or dates are invented here (see [`SECURITY.md`](SECURITY.md) *Supported
> versions*).

## How this changelog is maintained

- Every user-visible `feat:` / `fix:` / breaking change that lands on `main`
  adds an entry to **[Unreleased]** under the matching heading — *Added*,
  *Changed*, *Deprecated*, *Removed*, *Fixed*, or *Security*. This is the
  human-readable counterpart to the [Conventional Commits](CONTRIBUTING.md)
  history; we do not dump git logs into it.
- At release time, the **[Unreleased]** entries move into a new, dated version
  section (`## [<release tag>] - YYYY-MM-DD`, latest first), and a fresh empty
  **[Unreleased]** is started.
- The **Security** heading is the public face of the advisory process: each
  entry links the relevant GitHub Security Advisory (GHSA) / OSV record from the
  flow in [`docs/security-advisories.md`](docs/security-advisories.md). The
  changelog never discloses a vulnerability ahead of its coordinated fix.

## [Unreleased]

Pending for the next release. Nothing below is published until that release is, and the
section is dated only then.

## [26.10.1] - 2026-10-02

### Before you upgrade

- Back up first. The database gains nullable columns (session runs, approvals, session claims) and a deployment
  settings record; no data is rewritten. Rolling back to 26.10.0 is not supported.
- On its first start, the engine records which modules this installation runs: the standard set plus every module that
  already holds data. It then restarts itself once, in the same process, to apply that selection.
- Agent tools no longer use a sign-in stored in the server account's home. Sign each tool in once from **AI tools** or
  with `olivares tool login <tool>`; the sign-in belongs to your organization.
- If managed Claude Code settings on this host set `disableAllHooks` or `allowManagedHooksOnly`, Claude Code sessions
  are refused until both are `false` or removed. `olivares doctor` names the file.
- `olivares upgrade` on 26.10.0 finds 26.10.1 by itself.

### New

- Modules: an installation runs only the modules it selects (**Settings > Edition & modules**). A module that is off
  keeps its data and runs no work; its routes answer `404 module_not_enabled`.
- Sessions run confined to their folder (Linux Landlock); stdio MCP servers too. Without Landlock, every run says so.
- Sign in Claude Code, Codex and Grok Build through the engine. Start Ollama and pull its models from the console.
- Sessions can message each other and hand work to the sessions they may reach.
- Vault secrets as environment variables for a session or a template. Values reach only the session's environment;
  output, approvals and audit show `[secret <name>]`.
- One approval queue: approve or reject in **Approvals** or with `olivares governance approvals approve|reject`.
- MCP servers: add, test and turn on a server, and choose which of its tools run without approval. By default every
  tool asks.
- CLI: `olivares session`, `olivares tool`, `olivares mcp`, `olivares login`/`logout`, `olivares audit ls`,
  `olivares reporting signing`. `olivares` alone shows this installation and the next step. No command was removed.
- Each session's turn cost is recorded.

### Changed

- The console opens on **Now** (approvals, budgets, kill switch). Setup is three steps: install a tool, sign it in,
  start a session. One **New session** dialog everywhere.
- A new session picks how it runs by itself: your tool sign-in first, then the provider key, then a local model, and
  says which.
- A Claude Code turn can be interrupted without stopping the session.
- API: additions only. No operation was removed; some requests gained optional fields.

### Security

- The console keeps its sign-in in HttpOnly cookies.
- Approval reviews show the full command; a command or MCP argument that masking would hide is refused, not shown
  partly.
- An approval cannot be granted after its session's turn ended, was interrupted or stopped.
- An organization's tool sign-in cannot be used by another organization.
- A session stops when its owner's directory groups change; a resume uses the owner's current access.
- Offboarding stops late SSO and SCIM changes from restoring access.
- Policy decision evidence is redacted before it is stored.

### Fixed

- Tool sign-in, profile resolution and background work on a default PostgreSQL installation.
- `olivares quickstart` on PostgreSQL; running `olivares db init` again no longer breaks the next start.
- The New session dialog with one tool ready and another not signed in.
- `olivares mcp enable` and `olivares reporting signing`.
- `olivares serve --seed-demo` restarts on the data it seeded.
- Native package upgrades keep hand-edited service overrides.
- The installer prints commands that work when its directory is not on `PATH`.
- The Git publish push API refuses a body with trailing content.
- Report signing says when a license is needed, and its refusal carries an error code.

## [26.10.0] - 2026-10-01

### Install and upgrade

- Release tags no longer start with `v`. Update any script or pin that expected the prefix:
  pin tags in the form `26.10.<patch>`.
- An installation of 26.9 or earlier updates by naming the release once:
  `olivares upgrade --endpoint https://github.com/olivaresai/olivares/releases/tag/26.10.0`. From
  26.10 on, plain `olivares upgrade` finds new releases by itself.
- The engine now listens on every network interface by default: `--listen` defaults to
  `:8443` and `--grpc-listen` to `:8444`. To keep it on the local host only, start it with
  `--listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444`; in Docker Compose set
  `OLIVARES_BIND=127.0.0.1` in `.env`. The packaged systemd and OpenRC units listen on every
  interface too; override with `OLIVARES_EXTRA_ARGS`. TLS, the one-time setup token and the
  other start-up protections are unchanged.
- Upgrade every replica at once: the database schema moves to version 14 and a binary from an
  earlier release refuses to open an upgraded database. To go back, restore the earlier
  database together with the earlier binary.
- PostgreSQL connection strings are now read the way PostgreSQL itself reads them (the driver
  is now pgx v5.11.0). Check every configured string before upgrading: in `postgres://` URLs,
  percent-encode a space as `%20`, `=` as `%3D` and `@` as `%40`, and expect `+` to mean a
  literal plus; in `host=... dbname=...` strings, write a literal backslash as `\\` and put
  values with spaces in quotes (`application_name='my app'`). Strings that relied on the old
  reading are refused with a parse error.
- Packaged installations run the engine under a dedicated `olivares-svc` service account. The
  installer validates the account's declaration and refuses to guess when an installation's
  state does not say exactly one account.
- `olivares first-boot` prints the address the console answers at and whether first setup is
  still pending, with no credentials and no network. While no administrator exists,
  `olivares first-boot --new-token` replaces the one-time setup token.
- `olivares doctor` now judges the installation it finds: on a local installation the checks
  that only apply to a service report `not_applicable`, and every remedy names something you
  can do on that kind of installation.
- New configuration keys: `OLIVARES_PDF_RENDER_TIMEOUT` (a duration; the default stays 30s and
  invalid values are refused by name) and `OLIVARES_SESSIONS_AGENT_LINK_LISTEN` (accepted by
  `config validate`; not used by this build).
- The binary is built with Go 1.26.8.
- The enrolled disaster-recovery restore control works on PostgreSQL 15, 16, 17 and 18; before,
  it accepted PostgreSQL 16 only. Its format is unchanged. On PostgreSQL 17 and 18 only the owner
  holds the new MAINTAIN privilege on it, and a control that any other role could maintain is
  refused.

### Console

- The console is rebuilt around the work: one screen lists every session grouped by what it
  needs (active, waiting for you, settled), a session opens into the story of its run with its
  checks, last turns and touched resources, and a composer starts or continues a session from
  there. Every session has a working link that survives a reload, the first screen after login
  offers the next action you are allowed to take, and the interface is fully translated in
  seven languages.
- The dark theme is now the default. Light and system remain choices in the same toggle, and
  the light theme keeps full parity.
- Header controls wrap onto a second line instead of covering the screen's name, rows open
  from the keyboard, tables give their columns room, the New session dialog scrolls its fields
  instead of the page, and unnamed things get a readable fallback word in every language
  instead of a raw identifier.
- Command-line output goes through one renderer, so the same fact reads the same everywhere;
  some command output that was formatted by hand in 26.9.0 may look different now, and the
  plain text is the stable form.
- Signing in again in the same browser can no longer be undone by a late answer from the earlier
  sign-in: an older login's result never replaces or logs out the newer one.

- Long names, identifiers and descriptions no longer vanish behind an ellipsis: they wrap, or
  shorten in the middle with the full text on hover and for screen readers. The API Playground
  stacks its panels on phones, the access map spaces its nodes to fit their labels, and the
  session list names each session by its goal or latest action.
- A session recording that does not exist shows a not-found message instead of an error.
- On a fresh installation with a single workspace, the console lets you open it directly;
  before, the workspace overview asked you to pick a workspace it did not offer.

### Sessions and providers

- The first hour works from the command line: `olivares provider add | test | rotate | bind |
  rm` registers a model provider and proves it before anything depends on it (a refused
  credential is offered `rotate`, never `bind`), and `olivares agent profile update | rm`,
  `olivares agent deploy` and `olivares agent tool install` take you from an empty
  installation to a governed session.
- The official Codex and Grok command-line clients install beside Claude Code with
  `olivares agent tool install`. The Grok download is verified end to end; the Codex origin
  cannot be verified automatically today, so the installer labels it unverified instead of
  claiming a check it cannot make.
- System administrators can install and update agent tools from the console: the new Agent tools
  page detects the tools on the host, shows the official release it would install and follows the
  install as a job. OpenCode and Ollama join Claude Code, Codex and Grok; their downloads are
  checked against the SHA-256 in the official release metadata, which proves the origin but is not
  a publisher signature. Installing requires a deployment administrator signed in at AAL3.
- Ollama can be registered as a local model provider without credentials: the Providers page tests
  the endpoint, lists its models and binds them to Codex profiles. Plain HTTP is accepted only for
  localhost, loopback and private addresses.
- Agent profiles can carry operator-owned orchestration grants for a workspace, and an agent
  identity can be launched from the console. Sessions hand work to each other through MCP tools
  that use the same governed offer, respond and acknowledge paths as the REST API.
- Inside a session, `olivares work` and the message commands use the session's own credential;
  they never fall back to a human login.
- A session that names no workspace now starts in its own empty private directory, which is
  removed when the session is released (files are kept if a workspace was registered under
  it).
- A session's usage and cost appear in the session record and, where a cost sink is wired, in
  the spend ledger.
- The product documents exactly which launch choices each tool receives (model, effort,
  permission mode): Claude Code receives all three; Codex, Grok and OpenCode receive the model
  and the effort. A run's provider profile, driver and execution environment can also be read
  through a new read-only API without holding the run itself.
- The model gateway has one written contract (one streaming call with tool use, usage
  accounting and cancellation) with classified errors — rate limit, context length,
  authentication, transient — and a conformance suite every driver must pass; the contract and
  a driver-writing guide are in the reference documentation.

### Identity

- An organization can no longer join an existing account on its own: inviting or granting a
  membership to an address that already has an account now asks for the holder's consent,
  which this release does not provide a way to give (the answer is `consent_required`). To
  give a new person everything in one step, create the account with its first membership:
  `POST /v1/users` with `tenant`, `role` and `workspace_id`, or
  `olivares users create --member-of ... --role ...`.
- Removing a member removes them from that organization only: their account, its passkeys and
  their memberships elsewhere are untouched, and the console action is now called *Remove from
  organization*. Until the removal finishes in the background the account cannot be added back
  (`409 retirement_pending`), and afterwards only the organization that created the account
  adds it back directly.
- Writes that store what an account may do read the account's standing first, refuse with
  `409 subject_retirement_active` while a named account is being removed, and accept at most
  64 accounts per write (`409 too_many_subjects` above that).
- SCIM writes an account's attributes only when this organization alone governs it;
  `active=false` removes the member from this organization, and deleting a member no longer
  disables the account behind it.
- Invite mode needs a mail destination (`OLIVARES_INVITE_MAIL_DESTINATION` in
  `OLIVARES_NOTIFY_CONFIG`) and a declared console address (`--public-url` or
  `OLIVARES_PUBLIC_URL`); without both, sending an invitation answers
  `409 invite_delivery_unavailable`. A pending invitation can be resent with a new secret.
- An erasure request from one organization removes that membership and leaves the account's
  global record to the deployment's own erasure process; the receipt says so.
- An invitation response no longer contains the redemption token or its link: the link reaches
  only the invitee's mailbox.
- Groups show where they come from (`provisioned_by`). A group owned by a provisioner such as
  SCIM or a directory cannot be rewritten, moved or deleted by another source in a way that
  changes the membership it grants: those SCIM and operator changes are refused with the existing
  403, and attribute-only SCIM updates still succeed. Groups created before this release keep their
  earlier behavior.
- Local accounts can add a TOTP second factor from the console (RFC 6238, any authenticator app)
  and receive single-use recovery codes. After the password, sign-in asks for a code; a wrong code
  can be retried, and repeated failures are throttled per account and per address. A deployment
  administrator can require a second factor for administrators (`olivares auth totp-policy set`),
  and `olivares users totp` and `olivares users totp-reset` show and reset an account's factor.
  TOTP adds a factor to a password sign-in: passkeys and PIV remain the only ways to reach AAL3,
  and a sign-in through an identity provider relies on that provider's own MFA.
- An organization administrator can see and reset the second factor only of accounts that belong
  to that organization; a shared account, or one held by another organization, is reset by a
  deployment administrator at AAL3.
- A sign-in that is waiting for its code is held in memory on the node that started it; after a
  restart, the user signs in again.

### Governance and audit

- `olivares policy replay` answers what a policy decided on a past date, from the recorded
  evidence rather than today's policy, so an allow can be explained after a revoke. The same
  reconstruction is available over the API and in the console.
- Content inspection also scans decoded tool-use arguments (every unescaped key and string,
  including URL and base64 variants) within fixed limits per request; when a limit is reached
  the content is marked unscanned and the stock policy refuses it. An explicit
  unscanned-allow rule keeps traffic flowing on the inspection that did complete.
- A right-to-erasure receipt now states what its verification examined (`residual_scan_depth`,
  `residual_scan_opened`, `residual_scan_applicable`, `data_classes`), and an erasure whose
  scan could not look where the request said to look closes as `completed_with_gaps` with
  `verify_ok: false` instead of counting as verified. Extension authors: the residual-scan
  seam returns a report type now, and two marker strings are reserved on `verify_reason`.
- Stored things must be of a known kind: a policy, revision or workflow step of a kind no
  module registers is refused, and such a row left by an earlier version or a restore blocks
  that organization's retirement until the row is migrated or removed.

### Connectors and MCP

- Tenant administrators manage MCP gateways from the console's MCP tab and the API
  (`/v1/console/mcp-gateway`): add, edit, test and remove upstream servers, and choose which tools
  each session gets. New servers and session tools start disabled. A test initializes the server
  and lists its tools; it never runs one. Changes and tests require AAL3. Upstream secrets are
  references to the organization's Secrets store (`store:mcp/<name>`), and the client's own bearer
  is never forwarded upstream.
- Without `OLIVARES_AGENT_GATEWAY_CONFIG`, the gateway configuration lives in the store and is
  managed from the console. A configuration file stays in charge, and read-only in the console,
  unless it selects `mcp_source: store`; a file and store declarations that conflict stop startup
  instead of merging.
- Gateway forwarding has fixed limits (128 contexts, 8 MiB replies, 60 seconds per client call; a
  test lists at most 128 tools over eight pages in 10 seconds), and a call whose outcome is unknown
  is never retried.
- The MCP client no longer follows a redirect from a Streamable HTTP server: a 301/302/303/307/
  308 with a `Location` fails the request instead of sending anything to the redirected
  address. Configure each server with its final endpoint URL.
- A request that cannot be sent now reports a fixed reason (`connection refused`, `invalid
  endpoint URL`) instead of echoing the URL.

### Appliance

- The Fedora 44 appliance images (server and desktop editions) are not published with this
  release. They follow in a 26.10 patch release, once their build and first boot pass in release
  CI.

### Security

- MCP gateways refuse upstream replies and subscription events that echo their authentication
  credential, including JSON-escaped values. The refusal is audited without the credential.
  Upstream authentication secrets shorter than 8 bytes are refused before contacting the server.
- An organization can no longer join an existing account without its holder's consent, act on
  an account beyond its own membership, or keep authority over a member it removed; the exact
  new behavior is under *Identity*.
- Sign-in through an organization's identity provider now works only for members of that
  organization whose address lies in an email domain the provider claims, and a provider can
  be activated only with at least one claimed domain. After upgrading, an active provider with
  no claimed domain signs nobody in until an administrator claims one; the deployment-wide
  provider is unchanged.
- Accepting an invitation now passes the same network and password policy as the login form;
  before, an address the policy refuses at the login form could enter through an invitation
  link.
- A hook request's agent is the one its credential proves; a request header never selects the
  policy. A hook that logs in with a user session is refused in an organization that has
  agent-scoped hook policy.
- Dependency advisories with a fixed release were applied: the Backstage connector pins
  `adm-zip` 0.6.1 ([GHSA-7q85-xj36-vmfc](https://github.com/advisories/GHSA-7q85-xj36-vmfc))
  and `colord` 2.9.4 ([GHSA-2wm5-q62r-hmrv](https://github.com/advisories/GHSA-2wm5-q62r-hmrv)).
  Neither package is part of the engine binary.
- Single sign-on no longer signs anyone into an existing account by email when that account is
  already bound to an identity-provider subject, and an OpenID Connect address is used to match
  an existing account only when the provider marks it verified (`email_verified: true`). A match
  on the exact provider subject is unchanged. After upgrading, an OpenID Connect provider that
  omits `email_verified` no longer links an existing unbound account by email: the user gets a
  new account where just-in-time provisioning is on, or is refused; bind the account first to
  keep it.
- Every operation on a single agent run — detail, events, live output, input, interrupt, stop,
  resume, cleanup and delete — is authorized against the run's stored workspace. A caller
  confined to another workspace receives not found, and the events endpoint no longer reveals
  that such a run exists. A caller who can read the run but may not act on it receives 403, so
  the console can show the action as denied; a run the caller cannot read still answers not found,
  in the same time.
- The console and the documentation site take the patched undici (8.10.2 and, inside
  miniflare, 7.29.1) for [GHSA-rfgv-xxqx-mfg5](https://github.com/advisories/GHSA-rfgv-xxqx-mfg5)
  and [GHSA-w293-vg96-wgc3](https://github.com/advisories/GHSA-w293-vg96-wgc3). Neither is part of
  the engine binary.
- A secret manager that returns the credential used to call it can no longer turn it into a new
  secret: a Vault rotation whose result equals or contains the authenticating token is refused,
  and every credential the connector sent is removed from error text before it is stored or
  logged.
- Diagnostics and status output no longer show a user name or password written into a configured
  URL, such as a threat-feed or JWKS address.
- Audit archive anchors accept only well-formed object ETags and version IDs (up to 64 and 1,024
  bytes); a value containing URL query separators, spaces or control characters is refused, so a
  storage endpoint cannot place a token in audit metadata.

### Fixed

- The engine starts and upgrades on PostgreSQL 18. PostgreSQL 18 names generated NOT NULL
  constraints differently, so the schema check now matches them by table, columns and definition
  instead of by name; a dropped NOT NULL or a renamed primary key is still refused.
- A reader confined to one workspace can read the event history of the agent runs it is allowed
  to read; before, the events endpoint answered 403. Runs it cannot read still answer not found,
  in the same time as runs that do not exist.
- The installer and the HTTPS bootstrap no longer stop with `cosign is required` on a host
  without cosign: they fetch cosign v2.6.4, accept it only when its SHA-256 matches the digest
  pinned in the script, verify the release and clean up afterwards. `--install-cosign` keeps
  the verified copy; `OLIVARES_COSIGN=/path/to/cosign` uses your own.
- The service installer no longer refuses to replace an existing service file on a host
  without `cmp`; it falls back to SHA-256 and names the missing tools when neither works.
- The engine writes one log format in every mode (logfmt, UTC timestamps), and
  `OLIVARES_LOG_LEVEL` now governs what the engine emits — a deployment that sets it will see
  its log volume follow the level.
- `--help` no longer shows a value placeholder for a flag that takes none (17 flags were
  affected; the CLI reference is regenerated in all seven languages).
- Reporting passes its organization-scoped data handle to external report sources again,
  restoring late-bound report data.
- The Spanish console and documentation describe evidence and routine schedules accurately
  (making alteration detectable; a minimum interval).
- `olivares release manifest` help examples name 26.10.0.

## [26.9.0] - 2026-09-16

### Added

- Live session consoles can interrupt an active typed-provider turn while keeping
  the process and draft available for the next turn. Work-bound runs send their
  exact lease fence; stale or uncertain results remain explicit. Fenced HTTP
  interruption events record the authenticated operator.
- Configurable native layouts. The signed service adapter (`install.sh --data-dir`)
  admits a custom data directory under a shape policy (absolute, canonical, at least
  two levels deep, no symlink components, parent never created under privilege) and
  records `"layout": "custom"` in the ownership manifest; systemd units quote a path
  with a space. `install-agentops.sh` honours `OLIVARES_DATA_DIR` and an explicitly
  selected `OLIVARES_WORKSPACE_DIR`: the drop-in is rendered from
  `packaging/service/agentops.conf` (claude `HOME`, token dir, `ReadWritePaths` for
  an external workspace), `agentops.env` points its token path at the selected data
  directory, and the drop-in, runtime env and workspace are recorded in the manifest.
  `olivares uninstall` admits a custom data directory only when the unit at its
  indexed path executes the engine with it (`ExecStart=`, `command_args=` or the
  launchd program; comments and other directives never count, ambiguity is refused)
  or when its own preserve recorded an uninstall witness beside the service config
  after removing that unit, so preserve then plan then purge works on one host and an
  interrupted purge is retryable; it lists the workspace as kept and removes the
  managed drop-in. `olivares doctor` gains an `agentops-layout` check over the same
  record and reads the unit's data directory with the same parser. Manifests written
  before these fields keep validating as default layouts.
- A data directory or workspace under `/home`, `/root` or `/run/user` is rendered with
  `ProtectHome=tmpfs` and `BindPaths=` for exactly that directory; one under `/tmp` or
  `/var/tmp` keeps `PrivateTmp=true` and gets `BindPaths=` for that directory alone, so
  the rest of the host's temporary tree stays hidden (systemd 235 and later — the
  installers read `systemctl --version` and refuse an older host, naming the version).
  One under `/dev`, `/proc` or `/sys` is refused: kernel and device interfaces, not
  durable state. A path re-exposed with `BindPaths=` may not contain `:`.
- **Provider-profile administration in the console.** The sessions workspace
  (`/agentops`, `/sessions`) gains a *Provider profiles* tab, offered on
  `sessions:profile:read`: list (paged, filtered by state on the server), register a
  profile for homes that already exist on this node (the server validates the paths;
  nothing is installed or logged in), rename, disable/enable, retire (irreversible,
  confirmed by typing), and an admin-only *Reveal configuration* that fetches the stored
  homes on demand and drops them when hidden — the ordinary list and detail never carry
  a path. Each profile's source bindings sit beside it: list, bind (the roster row's
  persistent id and the revision this node applied, never a name) and revoke, on the
  independent `sessions:profile-binding:*` tiers, with the deployment-wide source
  administration a binding also needs stated up front. Server facts
  (`local_environment`, `operable`, `state`) are rendered as reported. To make an
  honest bind possible, `GET /v1/console/sources` now reports each row's persistent
  `id` and the `applied_revision` this node's reconciler wired (absent when not
  applied here). The plane also has two doors of its own — `/provider-profiles` on
  `sessions:profile:read` and `/provider-bindings` on
  `sessions:profile-binding:read` — so a principal holding only one of those tiers
  reaches it without any run or live-session permission. Whether a source may be
  bound is the engine's answer on the protected roster read made when Bind is
  pressed, never a client-side flag; confirmations, drafts and the on-demand
  configuration read end when the permission, tenant, principal or credential that
  allowed them changes.

- **Provider profiles and session identity (B1).** A provider profile is the durable
  identity of ONE configured provider instance on ONE execution environment: driver,
  owning environment and the canonical `config_home` / `user_home` the launched child
  runs under — configuration and storage identity, never an authenticated provider
  account. Profiles are administered under `/v1/m/sessions/provider-profiles` (rename
  keeps the id and the home; disable/enable are reversible; retire is final and frees
  the home for a NEW id; the paths appear only on the admin `configuration` read), and
  a source can be dedicated to a profile at the exact roster revision this node applied
  (`/provider-source-bindings`, keyed by the roster row's persistent id, never its
  name). A launch names `provider_profile_ref`: the server resolves and validates
  the homes, persists the non-secret snapshot on the run BEFORE the spawn, starts the
  child with `HOME` and the DRIVER's own configuration-home variable set to them, refuses
  a caller or gate that names any provider-home variable (`HOME`, `CLAUDE_CONFIG_DIR`,
  `CODEX_HOME`, `GROK_HOME`), and binds the provider's session id under the profile scope
  inside one transaction with the run row — so two homes may announce the same id
  and stay two sessions. Resume continues only on the same proven home. The event
  envelope carries the host-stamped registration snapshot of the configured source
  (`SourceRegistration`; a pushed collector envelope cannot supply one). Live rows
  are now unique per `(observation scope, external id)` with a partial unique on the
  managed canonical sid, via new module migrations for SQLite and PostgreSQL.
  Legacy runs are never assigned a profile after the fact. This entry introduced no
  provider runner of its own; the official Codex driver arrives with the entry below,
  and no Grok runner exists yet.
- **Profile-scoped sessions on every read surface (B2).** An observation now folds into
  the live row of its CHANNEL, computed by the server from the host-stamped source
  registration — `legacy` (no registration), `observed` (a source dedicated to a profile
  by a binding approved at host admission for the exact applied revision), `source`
  (a known registration with no verifiable profile) or `managed` (the plane's own bridge, for a run it
  launched; the only row carrying `canonical_sid` and `run_ref`) — so two homes that
  announce the same provider session id are two rows with two timelines. Every live
  row exposes `live_ref` and `attribution`, and new routes read exactly one row:
  `GET /v1/m/sessions/live/by-id/{live_ref}` and `…/timeline`, `GET /stream?live_ref=`
  (tenant-checked before the subscription opens) and `GET /runs?live_ref=` (the run the
  plane PROVED owns the row; nothing for an observed row). The bare external-id routes
  and `GET /runs?claude_session_id=` stay and become explicitly LEGACY: they answer for
  the legacy row and legacy runs only, never "the first" of several homes. A profiled
  run records the `live_ref` of its managed row once its id is proven; the
  credential→run→timeline export joins a profiled run through that row and a legacy
  run through its legacy events; `ReplayTimelineByLiveRef` replays one row. The console
  keys sessions by `live_ref`, opens a scoped row by it, shows attribution and profile
  per row, lists the observation rows that share a managed run's profile and id beside
  the run (never merged into it), and the launch dialog offers the active profiles —
  only the reference is posted, no profile is pre-selected. Composition now requires
  `provider_profile_ref` on the productive create endpoint; a node without an execution
  environment identity, an unknown/disabled/retired/foreign profile and a driver with
  no operated runner are still refused deny-closed.
- **The official Grok CLI is operated as a session driver, over ACP.** Naming the pinned
  official binary in `OLIVARES_SESSION_RUNTIME_GROK_BIN` REGISTERS the driver on that node,
  separately from Codex and from the Claude path: readiness stays per driver, with no shared
  switch and no binary resolved off the `PATH`. The run is an owned native child spawned as
  `agent --no-leader … stdio` — never a leader, a WebSocket server or a relay, because those
  share or expose a backend this run did not create — with `GROK_HOME` and `HOME` from the
  selected profile and the provider's own `GROK_DISABLE_AUTOUPDATER=1` pinning the child's
  version. Model and effort stay provider-owned open strings on the official agent flags.
  It runs through the SAME controls a Codex run does: the durable Claim holder and fence, the
  launch generation, per-run serialization, the immutable profile/environment/home identity,
  the deny-closed approval gate and the current-profile re-read inside every holder effect's
  authority transaction. Authentication is selected from the profile's AUTHORIZED source and
  never from the advertisement — `provider_account_home` uses the agent's cached-login method,
  `managed_injection` uses the injected-key method, there is no fallback between them, and the
  interactive browser sign-in is never started; a profile with no compatible advertised method
  is reported `auth_required` rather than inferred ready from a home that exists.
  **Two protocol facts are handled differently from Codex on purpose.** ACP's `session/prompt`
  response is the TURN'S COMPLETION, not its receipt, so the prompt is dispatched and `input`
  answers 202 as soon as the frame is written: a normal long turn no longer looks like a
  gateway timeout and never holds the per-run lock, so `input`, `interrupt` and `stop` keep
  answering while the model works; a second turn is refused 409 before any byte. And
  `session/cancel` is a notification with no acknowledgement, so an interrupt resolves pending
  approvals, cancels, and leaves the turn open until the prompt's own correlated result
  returns — the owned process stays usable for the next turn. Resume continues the EXACT
  stored conversation through the advertised resume capability (or `session/load` when resume
  is not advertised, chosen once), accepts the null/absent session id the protocol permits,
  refuses a response that names a different conversation, and never falls back to starting a
  new one; replayed history is not reported as live output. Permission requests are answered
  by selecting an option the agent OFFERED and the authority NAMED, with a persistent
  `allow_always` requiring an explicitly session-scoped decision; a malformed, duplicated or
  unknown-kind catalogue, an expired deadline, a moved authority and an unadvertised
  filesystem/terminal request all produce a valid protocol refusal that grants nothing.
  *Not claimed:* these behaviours are proven against an owned fake ACP child through the real
  HTTP, runtime, store and process group. Compatibility with an authenticated official Grok
  account is separate, later work and is not asserted here.
- **The official Codex CLI is operated as a session driver.** Naming the pinned official
  binary in `OLIVARES_SESSION_RUNTIME_CODEX_BIN` REGISTERS the driver on that node, and
  registration is what makes a `codex` provider profile launchable — readiness is per
  driver, with no shared switch and no binary resolved off the `PATH`. The run is an
  owned native child spoken to over the official JSON-RPC app-server protocol: launch,
  observation, pending approval answered in that method's own codec against the turn the
  pump observed, continuation, a typed fenced turn interrupt that ends the turn without
  ending the process or the conversation, stop, and runtime cleanup that still reaps the
  child after holder authority is gone. It runs through the SAME controls a Claude run
  does — the durable Claim holder and fence, the launch generation, the K2 work stamp,
  per-run serialization of every control, the immutable profile/environment/home identity
  and the legacy raw-input plane — and every holder effect first re-reads the run's
  current provider profile inside its authority transaction, so a retired or deleted
  profile refuses before any provider effect while `disabled`, a rename and a re-authorized
  `auth_source` deliberately do not revoke a live child. A profile now also carries its
  AUTHORIZED authentication source (`provider_account_home` or `managed_injection`, with no
  fallback between them and no default), the run persists the source it launched under
  before the spawn, and resume refuses when that authorization has moved since. The three
  run controls publish what they actually answer: `input` returns 202 with a closed
  `{accepted}` body and no longer advertises a 200 it never returns, `interrupt` and `stop`
  return the run resource, and all three publish the 503 UNKNOWN work-error envelope. The
  distinction between a refusal before any provider effect and an UNKNOWN after a possible
  one is preserved; an UNKNOWN is not downgraded to a refusal. The generated SDK wrappers
  remain generic maps — this changes the published contract they are built from, not their
  signatures.

### Fixed

- The packaged OpenRC unit now brings up loopback, re-owns `/var/lib/olivares`
  before start, and logs to `/var/log/olivares.log`. The `.apk` no longer ships
  a `root:root` data-directory node (apk was resetting ownership after
  post-install so `olivares serve` died with permission denied). `.deb`/`.rpm`
  still ship that directory node.
- Native `.apk` packages built from this source now ship an executable OpenRC
  unit at `/etc/init.d/olivares` with the `olivares` service account, a usable
  `/etc/olivares/olivares.env`, and hooks that record `init=openrc`. They do not
  enable or start the service. `.deb`/`.rpm` keep the systemd unit. Published
  v26.8.0 `.apk` assets still shipped the systemd unit and skipped service stop
  on Alpine; that historical payload is unchanged.
- OpenRC uninstall corroboration reads `command_args_base=` as well as
  `command_args=`, so a custom data directory rendered into the packaged or
  signed-archive unit still witnesses the estate after extra flags moved into
  `start_pre`. The base counts only where a `command_args=` assignment copies
  it in as the whole word `$command_args_base`, and every `command_args=`
  assignment must prove the same directory: an unreferenced or later-replaced
  base, or a single-quoted `'$command_args_base'`, refuses instead of
  corroborating an estate the unit does not start.
- Installing a second native estate over a preserved first one no longer leaves the first
  estate's generated inference-token path in force. `/etc/olivares/agentops.env` is one
  fixed path shared by every estate, so `install-agentops.sh` now decides that single
  `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` assignment by ownership: it re-points a value that
  is still exactly the default it generated for another estate (proved by the generated
  marker plus that estate's own ownership record carrying this file as a managed runtime
  env), preserves a value inside no estate as the deliberate external path the operator's
  refresher writes, preserves a file it did not generate, and refuses — before recording the
  layout and before the success banner — when the state contradicts the selected estate and
  no explicit selection was made, including a generated file whose value has the estate-default
  form for a directory that no longer answers with a record. Every other line of the file, comments included, is
  preserved in all cases. `OLIVARES_RUNTIME_TOKEN_FILE=estate|keep|<absolute path>` makes the
  choice explicit. The comparison follows the installed systemd version's
  `EnvironmentFile=` rules (quotes, escapes, continuations and last-wins), without sourcing
  the operator file; syntax whose version-dependent meaning cannot be established is refused
  before it can be called external. The byte rules follow each supported manager's own
  source: a double-quoted `\<CR>` is a continuation only before systemd 247 (from 247 the
  process receives backslash and CR, which no path contains, so the value is refused as no
  path), and 241–246 keep the backslash of any other escaped byte while 235–240 drop it. A
  NUL byte anywhere, or a key or value that is not valid UTF-8 in any assignment, makes
  systemd refuse the whole `EnvironmentFile=` (measured on 257: the strict form fails the
  unit and the `EnvironmentFile=-` the drop-in uses skips the file), so such a file is
  refused before any classification or re-point instead of being announced as wired;
  `keep` still preserves it byte-for-byte and says that no token path is wired, and
  `estate`/an absolute path repair it only when the token assignment itself carried the
  invalid bytes. Previously the installer warned, returned 0 and announced a wired
  co-deployment; purging the first estate then removed the token file the live service was
  configured to read.
- Optional workspace and template selections in the session launch dialog can be
  cleared after choosing them. Provider profile selection remains required.
- **Provider profile authority and replay.** Delayed process frames must still own
  the registered generation, current Claim and durable run before they can bind an
  alias or update managed liveness. Binding admission is fixed on each source
  observation before queueing: revocation stops new profile attribution, while an
  earlier envelope retains its exact historical binding during replay. Sandbox and
  evals accept an explicit `live_ref`, preserve it in their durable results, and keep
  legacy external-id selectors separate. Scoped findings are explicitly unavailable
  until their source can prove the same scope; an external-id match cannot award a
  passing score to another instance.
- Native AgentOps installation now provisions a missing systemd base unit through
  the verifying installer before applying the AgentOps configuration. An installed
  binary alone no longer counts as a complete service installation, and failed
  provisioning or requested startup is reported as a failure.
- Uninstalling a preserved custom estate no longer claims authority over whatever is
  installed now. Owning the recorded DATA and controlling the LIVE SERVICE are
  established by different evidence: the manifest (with its unit or the witness this
  product wrote) says which data directory this estate owns, and only a service
  definition at the closed unit path that executes this estate's engine says the
  installation is still the live one. When another installation holds that path its
  service is never stopped or disabled, its software is never removed and the system
  user and group it still uses are never deleted; when no definition is there at all,
  nothing is stopped because there is nothing to stop.
- An uninstall interrupted before it finished removing software now finishes on the
  retry instead of reporting success with the binary still installed. The witness
  record gained `pending_software` (schema `v2`) so it can say what an operation had
  not removed yet; a `v1` record still corroborates the data directory and no longer
  authorises any conclusion about software, which the plan discloses in that line.
- A service definition only witnesses a data directory when it is the directive the
  init system actually runs AND it runs this estate's engine: `ExecStart=` inside
  `[Service]` starting the recorded binary, `command_args=` beside a `command=` naming
  it, or a launchd `ProgramArguments` whose program is exactly the recorded wrapper.
  An `ExecStart=` in `[Unit]`, one carrying a systemd prefix character, and one running
  another program are refused by the engine (uninstall and doctor) and by
  `install-agentops.sh`, which tests the engine's name because it is reading the unit
  to discover the estate and has no ownership record to compare against yet.
- An engine upgrade no longer discards the recorded workspace and AgentOps entries when
  the previous ownership manifest is written in a different, equally valid JSON
  presentation. The adapter reads that record with a real JSON parser (POSIX awk, no new
  runtime dependency, and it never executes a staged binary to read a file), so compact,
  multi-line and reordered documents carry the same meaning; anything it cannot carry is
  refused with the record and the reason named instead of being dropped in silence.
- The same rewrite no longer overwrites the installer's own `$mode` with a file mode read
  from that record, which had made an upgraded estate record `"mode": ""` and the engine
  then refuse its own manifest with `unexpected install mode ""`.
- A data directory or workspace under `/tmp` or `/var/tmp` is no longer refused with the
  claim that no directive could reach it. That claim was false: systemd creates the
  destination of a bind mount since v235 (upstream `a227a4be`, which says so in its own
  message), so `BindPaths=` exposes exactly that directory inside the private `/tmp`
  while `PrivateTmp=true` keeps the rest hidden.
- Several sources of the SAME connector kind can now be registered and run at once.
  The runtime keyed every source by its connector's Descriptor name, so a second
  roster row of a kind was persisted, listed in the console — and refused by the
  engine. Sources are now registered under the operator's own name (the roster row's
  `name`), with the connector descriptor kept beside it, so `grok-home-a` and
  `grok-home-b` open with their own configuration and credential references, run
  their own connector instance and process, rotate, fail and stop independently.
  `sources plan` / `validate` no longer announce the one-instance-per-connector
  restriction the engine has stopped applying, and `as_source: true` on two identity
  entries served by one connector (`okta` and `entra` share `idp`) now wires both.
  This fixes several independent SOURCES; it does not by itself separate two
  provider sessions or config homes end to end.

### Changed

- The AgentOps drop-in carries a managed marker and is regenerated on reinstall from
  the recorded layout; a drop-in without the marker (operator-owned, or shipped by an
  earlier installer) is left untouched and named. A rerun without knobs recovers the
  layout from the installed unit and manifest, and a knob that contradicts the
  installed unit's data directory is refused instead of re-rendering around it.
  Privileged directory provisioning no longer follows links: every component is
  checked in the same privileged shell that creates the directory, owned directories
  under the data dir are asserted, and an existing external workspace is never
  chowned or re-moded. The signed adapter carries the recorded workspace, drop-in and
  runtime env across its manifest rewrite after validating them, so an engine upgrade
  no longer resets an explicitly selected external workspace or drops the ownership
  entries doctor and purge rely on.
- **Session launches require a provider profile.** The create dialog and productive
  API require explicit selection of an operable profile. Historical unprofiled runs
  remain readable and stoppable; resume refuses a run without a proven home instead
  of inheriting the service account's home.
- **Event provenance for configured sources.** `Event.Source` now carries the
  source's configured NAME instead of its connector's Descriptor name. Two sources
  of one kind were previously indistinguishable in the event stream; now they are.
  An exporter, filter or downstream consumer that matched a descriptor
  (e.g. `olivares.grok`) to select a configured source's events must match that
  source's name, or read the connector identity from the roster/runtime inspection
  surface, where it is still reported — the read API now returns it as a separate
  `component` field on each roster entry. Sources registered without an explicit
  name (and modules publishing their own events) are unchanged, and historical
  events are never rewritten or relabelled. `Event.Source` identifies an ingestion
  instance only: it is not authority for a provider, process, config home or user,
  and `EdgeObservation.Source` — the class of signal — is untouched.

## [26.8.0] - 2026-09-01

The first public release of Olivares AI — a beta, pre-1.0. Tag `v26.8.0` points at commit
`f443e084` of the public repository (tagged 2026-09-01T08:06:57Z, published 11:15:52Z);
release run `33485407512` finished with all eleven executed jobs green and
`publish-ota-manifest` skipped by its own gate. The changelog at the cut and the published
one at the tag are byte-identical, so every entry under **Added / Changed / Fixed / Security**
below is what this tag contains. APIs, schemas and the module surface may still change before
a stability commitment.

**What ships (64 release assets; the cosign-signed `checksums.txt` lists the 31 archives, packages and per-archive SBOMs, and the rest carry their own signatures, attestations and provenance):**

- Binary archives `olivares_26.8.0_<os>_<arch>.tar.gz` for linux and darwin × amd64 and arm64,
  plus `olivares_26.8.0_fips_<os>_<arch>.tar.gz` FIPS builds for the same four targets; each
  archive with SPDX and CycloneDX SBOMs, an SBOM in-toto attestation, an OpenVEX statement and
  its signed attestation.
- Native packages for linux amd64 and arm64: `.deb`, `.rpm`, `.apk` — binary, hardened systemd
  unit, example env file, no-login service user; the service is not auto-started.
- `checksums.txt` with its cosign signature and certificate, SLSA build provenance
  (`multiple.intoto.jsonl`), the image SBOM, an OpenVEX document for the set,
  `release-commit.txt` pinning the source commit, and the signed `stable-manifest.json` for the
  update channel.
- Container images `docker.io/olivaresai/olivares:26.8.0` (official coordinate) mirrored by
  digest to `ghcr.io/olivaresai/olivares:26.8.0`, with `-fips` and `-stig` variants and
  `latest`; registry tags carry no `v` prefix.
- Homebrew cask `olivaresai/tap/olivares` (macOS and Linux), and the one-command verified
  installer `scripts/install.sh` (cosign signature and SHA-256 checked before install).

**What the community binary contains, named for the roadmap:** the MCP catalog and admission
(`modules/catalog`, module XIV — approved agents, MCP servers, skills and templates, with MCP admission
policies), the MCP introspection source wired in the stock `serve` and the MCP `tools/call` gate; FinOps
budgets that deny or throttle spend, live in the default binary with no provisioning (`modules/finops`,
module XI); and compliance evidence mapping over 26 framework catalogs with sealed, exportable evidence
(`modules/compliance`, module XIII). All three run in the open binary with no license check
(`docs-site/src/content/docs/start/honesty-and-limits.md`, "The rest of the platform is open"); the
dir archive is WORM only on an immutable substrate, as the README states.

**What does not ship in this release, stated as absent:** Windows binaries (Linux container or
build from source); Apple notarization of the darwin binaries (the cask clears the Gatekeeper
quarantine); the Helm chart as an OCI artifact — its source ships in `deploy/helm/olivares` and
`release-chart.yml` runs only on a `chart-v*` tag that has not been cut; the hosted Cloud tier;
shadow mode and final work authority (design only); a general message bus for arbitrary agents
(messages stay scoped to an orchestration workflow, enforced by a boot test).

**Verify it:** `scripts/verify-release.sh` (keyless / Sigstore) or
`scripts/verify-release.sh --key cosign.pub --offline`; the chain per artifact type is in
[`docs/RELEASE-VERIFICATION.md`](docs/RELEASE-VERIFICATION.md).

### Added

- **Cross-session workflow and protocol interoperability** with durable K4 orchestration/runtime
  steps, generation-pinned A2A and MCP bindings, replay and subscription cursors, governed task
  lifecycle/reconciliation, bounded protocol Message projection, SQLite/PostgreSQL binding guards,
  and a visual protocol composer with closed mapping/loss declarations and server-derived
  validation.
- **Durable WorkItem execution ownership** with acquire, renew, release, takeover and revoke,
  monotonic fencing, database-clock rollback protection, restart recovery, and fenced input/stop
  for bound session runs.
- The control-plane engine: a single static Go binary (`olivares`) exposing
  a REST + gRPC API and a Terraform provider, with the React UI embedded.
- The differentiating pillar — the vendor-neutral, read-first **access map**
  (`agent → resource`, read vs read/write, `PERMITTED` vs `OBSERVED` drift).
- The append-only, hash-chained **audit ledger** with signed checkpoints and
  per-event signatures; dual store (SQLite single-node / Postgres with
  row-level security for multi-tenant).
- First-party connectors and the Apache-2.0 connector **SDK**.
- **Manage-as-code coverage** in the Terraform/OpenTofu provider: new resources
  `olivares_budget` (FinOps spend caps), `olivares_capability_config` (MCP-server
  connector configs, secrets by reference only) and `olivares_notification_route`
  (alerting routes), plus data sources `olivares_budgets` and `olivares_inventory`
  — each with import, drift detection, validation and acceptance tests.
  Registry docs are now autogenerated (tfplugindocs) from the schema + `examples/`.
- **GitOps reconciliation of the control plane's own desired state** as code
  (`deploy/gitops/olivares-as-code/`): the governance estate (agents,
  policies, budgets, connectors, routes) declared in HCL and continuously
  reconciled via Flux's tofu-controller or Argo CD, OpenGitOps 1.0-aligned.
  Actuating a deployment to real infrastructure remains human-in-the-loop-gated.
- A signed, verifiable **release pipeline** (cosign / Sigstore, SBOM attestation,
  OpenVEX, SLSA provenance, OpenSSF Scorecard, air-gap bundle) — see
  [`SECURITY.md`](SECURITY.md) and `scripts/verify-release.sh`.
- The **third-party connector ecosystem** (ADR-0016): the connector SDK declared
  **stable v1** with its own versioning policy (`sdk/VERSIONING.md`); a
  zero-dependency scaffold (`sdk/scaffold`, CLI `olivares-connector-new`)
  generating a complete out-of-tree connector repo with a standalone
  license-boundary check; **deny-closed signed admission of external connector
  plugins** at the host (operator-pinned sha256 + Sigstore/DSSE attestation
  verified against `connector_trust`, checksum re-pinned at exec — no observe
  mode, no unsigned escape hatch); a `connector` catalog entry kind with its own
  signed-admission policy/verdict pair and deny-closed approve gate (module
  XIV); and the curated **verified connectors index** in the docs site.
  `ContentSourceService` now puts governed knowledge document sources on the
  plugin wire with signed admission, ACL refs and live delta capability
  declaration; `CostSample.speed` and the `FindingReport` OWASP/ATLAS framework
  refs now cross the plugin wire (previously dropped silently).
- Project community-health and governance set: Code of Conduct, governance
  model, CODEOWNERS, support guide, issue/PR templates, and this changelog.
- **SARIF 2.1.0 findings export** (`GET /v1/m/security/findings/export?format=sarif`
  and `olivares findings export`), for a SOC that reviews governance findings in
  GitHub code scanning, GitLab, or Azure DevOps.
- **Receiver payload budget for the syslog connector** (`max_payload_bytes`,
  default `0` = off). A record over the operator-declared budget fails the
  delivery — retried, then dead-lettered — instead of being handed to a receiver
  that would split one auditable event into two unparseable halves. Reference
  values for the setting (RFC 5424 480/2048, ArcSight 1024, QRadar 4096) ship as
  documented constants.

### Changed

- **The community user cap is GONE: self-hosted user accounts are now unlimited in every
  edition.** The AGPL build used to admit three active accounts (the bootstrap superadmin
  plus two) and refuse the next one with `user_cap_requires_enterprise`; a lapsed or
  removed commercial license used to drop a deployment back to that same figure. Both are
  removed. `core/auth.CommunitySeatLimit` is now `0` (= unlimited),
  `enforceSeatCapTx` is an unconditional no-op, and the license-service downgrade guard no
  longer degrades anything — no license state (valid, expired, absent) can limit how many
  accounts a deployment runs, and none can disable or delete one. `license.Claims.MaxUsers`
  is display-only in every build. **This only ever widens what a deployment may do**;
  creating an account still requires exactly the RBAC permission it always did.
  The seat *seam* is deliberately kept as a compatibility no-op — `SeatPolicy`,
  `WithSeatPolicy`, `NewCommunitySeatPolicy`, `SeatLimit`, `enforceSeatCapTx`,
  `ErrUserCapRequiresEnterprise` (and its 403 code), `ErrLicenseDowngrade` (and its 409
  code) and the `acknowledge` parameters all remain, inert, so nothing that reads them
  breaks. The console's **Edition & license** panel now reports active-user USAGE with no
  denominator, no quota bar and no licensed-seat row, in all seven languages.
  (`LICENSING.md`; the commercial pricing canon is maintained privately.)

- **Every SIEM projection now carries the metadata commitment, and that commitment is
  BLINDED.** The chain hash has always committed to an event's metadata through a digest
  of the stored canonical metadata string, and the projections did not carry it — so a
  consumer holding one exported line could check chain linkage but could not recompute
  the hash. They carry it now (`olvMetaCommitment` in CEF/LEEF, `meta_commitment` in
  syslog SD, `ai.olivares.audit.meta_commitment` in both OTLP shapes and in OCSF
  `unmapped`), which completes the preimage on the wire. Byte-exact re-derivation is
  proven for `syslog` and the three OTLP spellings, for the value alphabets this ledger
  emits — neither is unconditional, since syslog substitutes a space for CR and LF and
  OTLP replaces invalid UTF-8, and nothing at the append boundary constrains the alphabet; `ocsf` (the eventing-sink default),
  `cef` and `leef` carry the same fields but are not yet byte-reconstructable, because
  their escaping and field mapping are lossy for free-text values. A row sealed BEFORE
  blinding existed carries no commitment key at all — omitted rather than empty, because
  an empty field is pseudo-evidence and its unblinded digest would be the very oracle
  the blind closes.
  The value is a hiding commitment over a per-record 256-bit blind, not the bare digest.
  An unblinded digest is a deterministic function of the metadata alone, so a holder of
  one line could confirm a guessed value by hashing it — and ledger metadata routinely
  carries guessable material such as a login IP or a denial reason — while two records
  with identical metadata would export the identical value, leaking an equality relation
  the projection deliberately withholds. The blind never leaves the store except inside
  the complete archive artifact.
  **Both hash rules stay live forever, and the record chooses.** A row sealed before
  blinding existed has no stored blind and keeps verifying under the unblinded rule; the
  new nullable `meta_blind` column is the discriminator. An append-only ledger cannot
  restate the hash rule of rows it has already sealed: every pre-existing row would stop
  verifying and a legitimate history would be indistinguishable from a forged one.
  Consequences, stated rather than implied: the long-horizon audit archive format is now
  `olivares.audit.archive.v2` with a `meta_blind` line field, and v1 archives remain
  verifiable permanently; `core/audit.FormatEvent` REFUSES an unsealed or half-populated
  event instead of emitting a line whose commitment field is empty; digest widths are
  now invariants checked at the append boundary rather than silently zero-padded into
  the preimage; and the SIEM push DTO carries the commitment verbatim, so pushed bytes
  cannot drift from pulled bytes.
  What this does NOT claim: recomputing the preimage is not the same as verifying
  authenticity (that needs an externally trusted key) or completeness (that needs
  adjacent records and a checkpoint). The three remain separate claims.

- **The audit ledger exports a postable OTLP request** (`otlp_envelope`): each
  EVENT line is a complete OTLP/HTTP JSON `ExportLogsServiceRequest` with the resource
  identity and instrumentation scope a collector needs, verified by decoding the
  output with the official generated type and unknown fields rejected. When this
  entry landed the existing `otlp` format stayed a bare `LogRecord` projection for
  file/NDJSON consumption; the format-catalog entry below ends that split reading —
  `otlp` now names this same postable envelope on every surface, `otlp_envelope`
  remains as its exact alias, and the bare projection moved to `otlp_log_record`
  (ledger pull export only). At the time this entry landed the projection also kept
  its exact bytes for every timestamp in the normal domain, a guarantee the
  namespace-freeze entry below deliberately retired while nothing is published.
  OUTSIDE that domain byte compatibility was already NOT guaranteed, and where the bytes
  differ it is a fix rather than a regression: an ordinary pre-epoch instant previously
  encoded as a wrapped negative in an unsigned field, which the official OTLP decoder
  rejects; instants between the signed and unsigned ceilings now carry their true
  unsigned value instead of a wrapped negative; and instants beyond
  `2554-07-21T23:34:33.709551615Z` now encode as unknown (`0`) instead of a wrapped
  value — including the small positive values that read as early-1970 dates, of which
  `2554-07-21T23:34:34Z` → `290448384` is the witness the tests pin. At isolated wrap-to-zero inputs —
  exactly `MaxUint64+1` among them — the old and new bytes happen to coincide.
  No first-party PARSER of those bytes exists in this repository — first-party code creates,
  transports and posts them, but nothing here reads them back — and whether any third party
  parses them is unverified.
  Three limits stated on
  purpose: the pull
  *file* is NDJSON and its LAST line is Olivares' `{"export_complete":true,…}`
  marker, which is **not** a request — a loop that posts every line must skip it;
  the push target must be the collector's exact `/v1/logs` URL, since the sink
  posts the configured endpoint verbatim; and the generic sink counts any 2xx as
  delivered without reading the collector's partial-success response (the
  dedicated OTLP logs connector does read it).
- **Every audit-export dialect carries the canonical hashed timestamp, and the
  product attribute namespace froze on `ai.olivares.*`**. The chain hash
  covers `occurred_at` as canonical-layout TEXT (RFC 3339-shaped, nine
  fractional digits, fixed-width for four-digit years; a pre-year-1 or
  post-9999 instant renders wider yet stays framing-safe in every dialect,
  which a test proves), but no export
  dialect carried those bytes — CEF/LEEF/OCSF ship millisecond epochs and the
  syslog header caps at microseconds, so a sub-millisecond difference the hash
  distinguishes was invisible in every projection. Now CEF/LEEF carry
  `olvOccurredAt`, syslog a structured-data `occurred_at`, both OTLP shapes an
  `ai.olivares.audit.occurred_at` attribute and OCSF the same key under
  `unmapped` — verbatim, and proven by a test that renders two events one
  nanosecond apart. In the same pre-publication window the record-attribute
  namespace froze: `olivares.audit.*` → `ai.olivares.audit.*` (bare `olivares.*`,
  read as reverse DNS, claims a TLD the product does not own), the OCSF
  notification keys became `ai.olivares.event_type`/`ai.olivares.tenant.id`
  (OCSF parks every preserved caller field under `caller.`, because its
  `unmapped` map is shared with encoder-owned markers; OTLP keeps an ordinary
  caller key's natural spelling and quarantines only the live and retired
  product namespaces — a per-schema policy pinned by a cross-schema test, not
  an accidental divergence), the trace-viewer OTLP
  download and the GenAI inference spans moved their product keys under
  `ai.olivares.*`, and the hash DOMAIN SEPARATORS (`olivares.audit.v1` and
  friends) were deliberately left untouched — they are cryptographic domain
  tags, not attribute names. The OTLP envelope keeps
  `observedTimeUnixNano == timeUnixNano`, and that equality is now DOCUMENTED
  as a verified fact rather than a habit: the ledger's `OccurredAt` is
  server-assigned at append (no caller time field exists), so occurrence and
  first-party observation are the same instant — while the notification feed
  reports `0` because its event time is source-supplied. A draft of this
  change emitted `0` here too; adversarial review established that the stock
  Collector receiver does not backfill a zero observed time, so that draft
  would have deleted a known true value, and it was reverted before landing.
  The trace-viewer download additionally renders event-derived
  span attributes in sorted key order, so two downloads of one trace are
  byte-identical. Breaking on the wire, free today: nothing is published and
  no consumer exists; the new bytes are pinned by goldens and a namespace
  guard that fails on any bare pre-freeze key.
- **The CLI stopped hiding two export formats.** `olivares audit export` advertised,
  completed and error-messaged `cef|syslog|otlp` while the engine accepted five —
  so the LEEF ledger export, the one a QRadar shop needs to ingest the
  tamper-evident chain, was reachable but undiscoverable, and the error message
  said it did not exist. Every operator-facing list — help, flag usage, the
  invalid-format error, shell completion, the OpenAPI enum and the API's own error
  text — is now BUILT from one ordered registry in the engine, and the tests iterate
  that registry rather than repeating it, so the same drift cannot recur silently.
- **SIEM wire output — LEEF records now carry `devTime`** (13-digit epoch, no
  `devTimeFormat` needed) on both the ledger export and the notification feed,
  so QRadar times an event by when it happened rather than when it arrived. The
  attribute is omitted, never fabricated, for an event with no recorded time.
- **SIEM wire output — `sev`, `devTime` and `devTimeFormat` are now owned by the
  encoder.** A caller field with one of those names (in any case) is re-keyed to
  `olvSev` / `olvDevTime` / `olvDevTimeFormat`: its value still travels, but it
  can no longer override the normalized severity or re-date the event.
- **SIEM wire output — CEF header fields are bounded to the CEF V27 sizes**
  (vendor/product 63, version 31, event class id 1023, name 512). The spec does
  not say whether those count characters or wire octets, so both readings are
  honoured; a long non-ASCII device name or title is truncated further than the
  number suggests. The extension, which carries the auditable content, is never
  truncated.

- **The OTLP/HTTP JSON body is no longer produced by `protojson`, and its shape changed.**
  This affects every destination that emits the OTLP format, not only the OTLP transport.
  Five render through `siemfmt.OTLPLogJSON` — `filelog`, `splunkhec` with `format=otlp`,
  `s3archive`, `siemsink` and the generic `siem` output — and `otlplog` projects the same
  resolved request itself, so six destinations in total. What changed inside the record:
  `severityNumber` is always a present, UNQUOTED JSON number (OTLP/JSON requires integer
  enums and forbids the enum names, so `"severityNumber":"SEVERITY_NUMBER_ERROR"` was
  nonconformant, and the zero value was omitted entirely, giving a raw-JSON consumer two
  presence shapes for one column); `timeUnixNano`/`observedTimeUnixNano` are always present
  as quoted decimal strings with `"0"` for OTLP's "unknown"; and `severityText`, `eventName`
  and `body.stringValue` are present even when empty, for the same one-shape-per-value
  reason. The outer envelope is unchanged — `LogsData` and `ExportLogsServiceRequest` both
  carry `repeated ResourceLogs resource_logs = 1`, so the document still begins
  `{"resourceLogs":[…]}`. Emitting explicit defaults is a deliberate canonical PROFILE, not
  an OTLP requirement: a receiver decodes an omitted proto3 SCALAR default and an explicit one
  identically. The body is the exception and is deliberately not one: an omitted `body` decodes
  to a nil message while `{"body":{"stringValue":""}}` selects the AnyValue oneof, so the two
  are not `proto.Equal` — we emit the empty body on purpose, and it is a distinct statement,
  not a canonical spelling of absence. The JSON grammar, the timestamp conversion and the input
  validation live in `sdk/siemwire` (`OTLPTime`, `OTLPExportRequestJSON`), standard-library
  only, so the SDK's zero-dependency guarantee is unaffected. Output is byte-deterministic,
  which `protojson` explicitly does not promise across library versions.

- **The notification type moved into OTLP's dedicated `eventName` member.** It used to
  travel as a synthetic `eventType` ATTRIBUTE, which both ignored `LogRecord.event_name`
  (field 12 of the pinned `opentelemetry-proto`, whose presence marks a record as an Event)
  and could be shadowed by a caller-supplied field of the same name — a duplicate attribute
  key, which OTLP forbids and whose handling a receiver is explicitly free to decide. The
  `eventType` attribute is gone. The `ai.olivares.*` attribute namespace is now reserved: a
  caller field landing in it is re-homed under `caller.` rather than allowed to shadow a
  product key, an empty caller key is given a generated name rather than dropped, and the
  authoritative `Notification.Tenant` travels as `ai.olivares.tenant.id` so a caller field
  named `tenant` can no longer replace it. Every such move is recorded structurally (below).

- **The OTLP resource and instrumentation scope now say what they mean.** The bare `vendor`
  resource key — not an OpenTelemetry convention — became the namespaced
  `ai.olivares.device.vendor`; it is deliberately NOT mapped to `service.namespace`, which
  means a deployment grouping rather than display branding. The CEF/LEEF header revision
  likewise moved to `ai.olivares.device.version`, and `service.version` is now emitted ONLY
  when the running service's version is actually known (`Device.ServiceVersion`) — the
  semantic conventions define it as the service's API or implementation version, and an
  operator may set the device header to a reseller's branding revision, so an absent
  attribute is honest where the old one asserted something false. The instrumentation scope is
  `ai.olivares.siemfmt` with its own encoder version, instead of being named after one
  transport and versioned with the operator-configurable device header: six destinations emit
  through this encoder, and a backend that groups records or selects a schema by scope
  version was previously told the reseller's CEF header revision.

- **The OTLP protobuf encoding keeps the same mechanism and gains the same timestamp
  guard.** It still marshals the generated types, and `siemfmt.OTLPLogsData` is retained as
  the typed source (with `OTLPLogsDataFrom` added so a caller that needs both encodings
  resolves the notification once). What changed is the VALUE it carries for an instant
  outside OTLP's representable range: previously a wrapped non-zero number, now `0` — which
  proto3 then omits from the wire entirely. So binary payloads do change, deliberately, for
  exactly those instants, and representable timestamps are untouched. (Other fields change in
  BOTH encodings for the separate reasons above — the event name, the resource keys and the
  scope — so this note is about the timestamp specifically, not a claim that nothing else
  moved.) A test decodes both real encodings and compares the WHOLE messages with
  `proto.Equal`, and another pins the absolute value the typed projection emits at each edge.

- **The format-token catalog: `otlp` now selects the complete, postable OTLP/HTTP
  JSON `ExportLogsServiceRequest` envelope on every surface.** Until this change
  the one token named two wire shapes at once — the bare per-line `LogRecord`
  projection on the ledger export and the full request envelope on the notification
  connectors — so "export as otlp, then post it" was correct advice on one surface
  and a guaranteed collector rejection on the other. One token, one wire shape is
  the contract now: `otlp_envelope` stays accepted as an exact byte-for-byte alias
  of `otlp` everywhere (resolved at encoder selection; stored configuration and
  audit records keep the spelling the operator wrote), and the former projection
  keeps its honest value under the NEW token `otlp_log_record` — byte-identical to
  what the ledger's `otlp` used to emit, and offered ONLY on the ledger pull
  export, because a bare `LogRecord` line is not a postable `/v1/logs` body, so
  the eventing sink and the push forwarders deliberately do not declare it. Every
  surface now derives its accepted set, ordering and default from one catalog
  (`sdk/siemwire`) instead of six hand-maintained copies: the ledger export accepts
  `cef|leef|syslog|otlp|otlp_envelope|otlp_log_record|ocsf` (default `cef`), the
  eventing sink `ocsf|cef|leef|syslog|otlp|otlp_envelope|json` (default `ocsf`;
  `json` is the raw passthrough, eventing-only), the notification connectors
  `json|cef|leef|syslog|otlp|otlp_envelope|ocsf|asim` (default `json`), and the
  syslog connector `syslog|cef|leef` (default `syslog`). Deriving from the catalog
  equalized a recorded drift UP: `s3archive` now accepts `asim` like its sibling
  connectors. Breaking, deliberately: an unknown or corrupted stored format value
  is now a deny-closed error — at authoring/configuration time it names the
  surface's accepted list. The old behavior varied by connector: `filelog`,
  `splunkhec` and `s3archive` silently relabelled it as JSON output, the syslog
  connector fell back to its native RFC 5424 record, and the generic `siem`
  connector already refused it; the silent cases turned a typo into an unnoticed
  schema change at the SIEM. Two version-relative notes for a beta operator: a stored
  eventing subscription whose sink_format is the exact spelling `otlp` and which
  delivers `audit.recorded` events changes wire shape (bare line → envelope), and
  the engine logs ONE structured warning per such subscription naming both shapes;
  and audit metadata recorded before this change (for example `export_format` in
  `audit.export` events) reads under the token's OLD meaning — the record shows
  what the operator selected then, not what the token selects now.

### Fixed

- **NHI ownership, roster filtering, and recording search now preserve deliberate operator input.**
  Ownership dialogs refuse no-op and apparent-clear submissions while retaining the sponsor
  required for agent updates. Client-only principal filters follow roster cursors and stop after a
  failed page until explicit retry. Recording subject/grant search runs only on submit, persists in
  the URL, and does not issue an audited list read for each keystroke; `%`, `_`, and `\` in
  `subject_contains` are escaped as literal text with an explicit SQL `ESCAPE` clause.

- **An OTLP timestamp could report a wrong date that decoders accept.** The notification
  encoder cast `time.UnixNano()` — int64, and documented as undefined outside 1678-2262 — to
  the unsigned OTLP field. A pre-epoch event was not rejected but silently relabelled:
  1969-07-20T20:17:00Z was emitted as `18432561093709551616`, which reads as
  `2554-02-07T19:51:33.709551616Z`. An instant past `2554-07-21T23:34:33.709551615Z` wrapped
  the other way: the next whole second after the ceiling, `2554-07-21T23:34:34Z`, became
  `290448384` — `1970-01-01T00:00:00.290448384Z`. (That instant is 290,448,385 ns past the
  ceiling, not one second; exactly one second past it would have wrapped to `999999999`.) Both are valid unsigned
  values, and the protobuf and JSON wire types are both plain unsigned 64-bit, so the generated
  decoder accepts both of those wrapped numbers; whether a receiving collector applies its own
  range validation on top is destination-specific. `siemwire.OTLPTime` builds the value from Unix seconds
  plus the nanosecond remainder, checking each bound before the arithmetic that could
  overflow past it, and reserves `0` for instants with no OTLP representation.
  The ledger export had the same class of defect in a different shape — it formatted the
  SIGNED nanoseconds, so a pre-epoch ledger event emitted a NEGATIVE number into a field OTLP
  declares unsigned, which a strict decoder rejects outright rather than mis-dating — and that
  was fixed separately, with its own private copy of this algorithm in `core/audit`.
  **The two are not yet ONE definition**, and until they are converged nothing here says they
  are.

- **A time OTLP cannot express is no longer silently dropped either.** `0` alone would have
  traded a wrong date for a MISSING one, and a backend that substitutes ingestion time for a
  missing timestamp would then index a 1969 event as today's. `OTLPTime` therefore reports
  WHICH situation produced its value — absent, exact, the epoch (which OTLP cannot
  distinguish from unknown, since its "unknown" IS 0), before the epoch, or after the uint64
  ceiling — and the notification feed carries the authoritative instant for the epoch,
  pre-epoch and post-ceiling cases. One limit is inherent and stated rather than hidden: Go's
  zero `time.Time` IS the real instant `0001-01-01T00:00:00Z`, so a notification genuinely
  timestamped in year 1 is indistinguishable from one carrying no time and gets no fallback.
  The attributes are: `ai.olivares.event.time.unix_seconds` and
  `ai.olivares.event.time.nanos` as machine-comparable integers, the reason in
  `ai.olivares.event.time.status`, and `ai.olivares.event.time.rfc3339` as the human form —
  the last **only when it is genuinely RFC 3339**, because Go's `RFC3339Nano` layout happily
  formats year 10000 and year -1 into text no RFC 3339 parser accepts, so the value is
  parsed back before it is emitted. There is deliberately no "the uint64 it would have had":
  outside the range no such value exists, and a wrapped one would recreate the very bug this
  work removes.

- **A deeply nested attribute value could take the process down.** `sdk/siemwire` walks the
  OTLP `AnyValue` union recursively in both its validator and its encoder, and an unbounded
  walk exhausted the goroutine stack — which in Go is a FATAL error no `recover()` catches, so
  the control plane died instead of rejecting a record. It is reachable from the public SDK
  third-party connectors build against. The nesting limit is now DECLARED (`maxOTLPValueDepth`,
  32) and exceeding it is a precise error naming the path. Thirty-two is a chosen defensive
  budget, not a measured universal maximum: the fixtures in this repository nest at most three
  levels, and nothing here can establish a bound on every shape a caller might build. Measured: unbounded, a 200 000-level value killed the test process after 52 seconds;
  bounded, it returns an error in 0.022 seconds.

- **A value that JSON cannot carry unchanged is no longer altered invisibly, and the
  original is preserved.** `encoding/json` replaces every invalid UTF-8 sequence with U+FFFD,
  so `"x\xff"` and `"x\xfe"` serialized to identical bytes with nothing to indicate it —
  while the CEF and RFC 5424 syslog renderings of the SAME event, being byte-oriented, did
  not perform that substitution. `siemwire.ValidateOTLPRequest` (now exported, and applied to
  BOTH projections rather than only the JSON one) REJECTS invalid UTF-8, duplicate attribute
  keys and empty attribute keys, naming the offending field, so the grammar layer never
  repairs input behind a caller's back and the two encodings cannot have different contracts.
  The notification feed substitutes explicitly instead of dropping the event — losing a
  governance record from one feed is worse — and records every adjustment in the structured
  `ai.olivares.wire.adjustments`: one entry per altered input or key — not one per primitive
  transformation, since a key that is sanitised, then re-homed, then de-duplicated yields a
  single entry recording its original bytes and final emitted name — each carrying the
  operation, the exact location, the emitted value, and the ORIGINAL bytes in an OTLP
  `bytesValue` (base64 in OTLP/JSON). A comma-joined string could not do this: a caller's own key may contain any
  delimiter, and a string cannot carry bytes that were not valid UTF-8 to begin with.
  `ai.olivares.wire.adjustment.count` gives the number of ENTRIES, for a rule that only needs
  to know a record was altered.

### Security

- No published advisories yet. When a vulnerability is fixed, it will be listed
  here with its GHSA / OSV identifier per
  [`docs/security-advisories.md`](docs/security-advisories.md). The vulnerability
  remediation SLA and supply-chain integrity controls are documented in
  [`SECURITY.md`](SECURITY.md).

[Unreleased]: #unreleased
[26.10.0]: https://github.com/olivaresai/olivares/releases/tag/26.10.0
[26.9.0]: https://github.com/olivaresai/olivares/releases/tag/v26.9.0
[26.8.0]: https://github.com/olivaresai/olivares/releases/tag/v26.8.0
