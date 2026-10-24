# Provider profile model catalog

`GET /v1/m/sessions/provider-profiles/{ref}/models` requires
`sessions:profile:read` in the profile's tenant. It reads the selected tool's
native catalog in the profile's product account home, or the bound provider's
models endpoint. It sends no prompts and creates no provider conversation.

A successful read returns `profile_ref`, `driver`, `state`, `models`,
`default_model`, `free_text`, `checked_at`, and `refresh_interval_seconds`.
The catalog is cached per tenant and profile for 120 seconds. Add
`?refresh=true` to read upstream again. Concurrent reads for the same profile
share the in-flight read. HTTP responses use `Cache-Control: no-store`.

Each model has an `id`. The following fields appear only when reported by the
tool: `display_name`, `effort_levels`, `default_effort`, `is_default`, `is_new`,
`is_legacy`, `supports_fast_mode`, and `service_tiers`. Effort and service-tier
values are open strings: a vendor can add one without an Olivares update.
Missing markers mean unknown. No model name implies a capability or legacy status.
An empty `default_model` delegates to the tool's own default; callers should
always offer that choice. Free text remains available with `free_text: true`.

- Claude Code: Agent SDK `initialize` model information, following its native
  sign-in status read. Its `default` selector is marked as the default. Claude
  Code 2.1.296 reports no new, legacy or default-effort marker, so
  `is_new`, `is_legacy` and `default_effort` stay absent for it and a caller must
  not guess them. Configured hooks and MCP are disabled for discovery.
- Codex: stdio app-server `initialize`, `account/read`, and paginated `model/list`.
  Hidden models are excluded; no thread or turn is started. Codex 0.162.1 sends
  no `isNew` or `isLegacy`. `is_legacy` is true when the model names a newer one
  in `upgrade`; `is_new` is true when it carries `availabilityNux`, the tool's
  own announcement of a model to try; both are false otherwise.
- OpenCode: default/free-text fallback. Its native `models --verbose` command
  can load configured plugins; it is not run by this read. Discovery can be
  enabled when a native catalog interface disables all executable customization.
- Grok: native `models` command, including its reported default marker.
- Bound API provider: the existing sealed provider probe, using only the
  registered endpoint and credential. Manual connection-test receipts stay unchanged.

`state: unsupported` explains that the tool cannot supply a catalog and offers
its default or free text. `state: failed` returns a fixed connection/sign-in
message and an empty model list. Neither exposes vendor error text, paths,
account details, or credentials. Disabled, retired, foreign-environment, and
revoked bindings are rejected even when a catalog was cached. Results are
discarded when a profile, login metadata, or provider record changes during the read.

Existing profile routes, launch fields, and ID-only discovery readers retain
their contracts. This read supplies choices; it does not change a running
session's model or effort.

Launch choices (`POST /v1/m/sessions/runs`):

- `model` is passed to the tool as written, and the tool validates it, with one
  exception. A Claude Code launch on the tool's own sign-in never hands the tool
  an alias: Claude Code resolves an alias, or picks its default when no model is
  named, in each process it starts, and the result was measured to differ
  between sessions of one account. The engine launches and records the exact id
  instead: `default` or no model is `claude-opus-5-5`, `opus` is
  `claude-opus-5-5`, `sonnet` is `claude-sonnet-5-5`, `haiku` is
  `claude-haiku-5-5` and `fable` is `claude-fable-5-1`, keeping a `[1m]`
  suffix. An alias stays accepted on input and the run's `model_ref` is the id.
  A provider-bound launch keeps the provider's model names. Enabling Fast mode
  checks the reported capability of the chosen selector, including the catalog's
  default when no model was chosen, and pins its exact id.
- `effort` is an open term for Codex and the other drivers. Claude Code
  accepts only `low`, `medium`, `high`, `xhigh` and `max`; a vendor effort
  outside that set can be listed here but is refused at launch with 400. This
  five-value set is the final Claude launch contract. The console must keep
  filtering its reported effort list to those accepted values.
- Optional `fast_mode` is a Claude Code choice: `true` or `false` is carried in
  its native `fastMode` settings. Omitted or null keeps the tool default;
  explicit false turns it off. True needs a model whose catalog entry reports
  `supports_fast_mode: true`. Failed discovery returns 503, and an unsupported
  or unknown capability returns 400. Provider-bound launches refuse true:
  Claude's Fast-mode availability check reaches Anthropic directly instead of
  following the bound provider endpoint. Other drivers refuse either boolean.
- Optional `service_tier` is a Codex choice. A non-empty value from its model's
  `service_tiers` is passed unchanged on native `thread/start` and
  `thread/resume`. Values are bounded to 200 bytes with no whitespace or control
  characters. Empty, omitted or null keeps the tool default; other drivers
  refuse a non-empty choice. Codex must confirm the requested tier in its
  response before the engine sends a prompt; a fallback is refused.
- Both choices are returned on the run and survive stop/resume and queued
  launches. Nullable storage preserves existing sessions without inventing a
  Fast-mode or tier preference. Both New session forms and the session message
  bar offer the matching control when the selected model reports support, before
  advanced options. Provider-bound profiles do not offer Fast On. Each control
  offers the tool default; a changed profile or model cannot receive another
  selection's choice. Unavailable choices block launch and offer the tool default.
- There is no mid-session settings operation. The message bar shows the session's
  choices; changing its model, effort, Fast mode or tier opens a new session with
  that preset. The running session keeps its settings.

Native interfaces: [Claude Code Fast mode](https://code.claude.com/docs/en/fast-mode)
and [Codex thread protocol](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/app-server-protocol/src/protocol/v2/thread.rs).
The tool still decides account availability and its own speed restrictions.
