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

What a launch honors today (`model` and `effort` on `CreateRunRequest`):

- `model` is passed to the tool as written; the tool validates it.
- `effort` is an open term for Codex and the other drivers. Claude Code
  accepts only `low`, `medium`, `high`, `xhigh` and `max`; a vendor effort
  outside that set can be listed here but is refused at launch with 400.
- There is no launch or update field for Fast mode or a service tier.
  `supports_fast_mode` and `service_tiers` are informational: a client must not
  offer a toggle for them until a launch field exists.
- There is no mid-session settings operation. A client that wants another model
  or effort starts a new session with that preset.
