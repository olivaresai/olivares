# API errors

Ordinary REST failures use the `Error` schema in `/openapi.json` and
`/openapi.beta.json`:

```json
{"error":{"code":"not_found","message":"not found"}}
```

Both `code` and `message` are strings. Use the HTTP status and `error.code` to
classify a failure; `message` is human-readable. Beta handlers that previously
provided only a message use `module_error` until they define a more specific
code. Existing specific codes retain their meaning.

When a person can act on the failure, the optional `error.remedy` names the one
next step, such as the edition and where to get it, or the factor to enroll:

```json
{"error":{"code":"business_required","message":"Session recording requires Olivares Business.","remedy":"Available in Olivares Business: https://olivares.ai/pricing"}}
```

A remedy is fixed text chosen by the code alone, so it never repeats what the
failure carried. A code with nothing to add omits it. The CLI prints it as the
second line of an error.

An error may include additional fields. Git publication keeps `intent_id` at the
top level so callers can inspect an unresolved operation before deciding what to
do next. Workflow graph validation keeps `error.step_ref`; policy failures keep
their policy references. Clients should retain such fields and tolerate new ones.

Some operations return a structured result on a non-success HTTP status, such as
a raced session control request or a rejected publication intent. Preserve that
result and consult the operation's response schema. An HTTP status alone does
not determine the response body. SCIM and OAuth retain their protocol-specific
error formats.

The beta migration is recorded under Changed in [CHANGELOG.md](../CHANGELOG.md),
which names the operations that answered a string-valued `error` in 26.10.1<!-- release-fixed -->.
Clients reading older servers can accept both a string-valued `error` and the
object envelope.
Stable routes keep their existing bodies.

Handler authors use `api.WriteJSON` for encoding and `api.ErrorBody` for an error
envelope. Supply only a safe client-facing message; the constructor does not
sanitize raw internal errors. Keep route-specific cache headers, media types and
structured results at the handler boundary.

For JSON request decode failures, handlers retain their HTTP status, error code,
and envelope. The message names the offending JSON field when the decoder can
identify it, such as `invalid JSON body: unknown field "request.action"` or
`invalid JSON body: invalid value for field "request.principal"`. It does not
echo field values or Go type names. Syntax errors, empty required bodies, size
limits and trailing documents retain the handler's existing generic message.
Custom unknown-field errors may have only a leaf name. Custom type errors
retain the generic message because their offsets may be relative to one value.

Handler authors should pass decoder failures through
`api.RequestBodyErrorMessage(err, existingMessage)`; it appends only classified
field diagnostics and keeps the supplied fallback for other failures.

## Error codes

Every code the engine's handlers return, with its remedy. The table is generated
from the engine's error code table (`core/api/errorremedy.go`), and a test fails
when a handler returns a code that is not in it.

<!-- BEGIN GENERATED olivares-api-error-codes — regenerate with `OLIVARES_UPDATE_GOLDEN=1 go test ./core/api -run '^TestErrorCodeDocsMatchTheTable$'`; do not edit by hand -->

| Code | Remedy |
| --- | --- |
| `account_login_refused` |  |
| `accounts_at_limit` |  |
| `activation_invalid_request` | Correct what the message names, then send the request again. |
| `activation_not_recorded` |  |
| `activation_restart_unavailable` |  |
| `activation_unavailable` |  |
| `active_content_unapproved` |  |
| `addon_requires_license` | Add this add-on to your license: https://olivares.ai/pricing |
| `already_running` |  |
| `approval_denied` |  |
| `approval_required` |  |
| `approval_unavailable` |  |
| `ask_dlp_denied` |  |
| `ask_model_changed` |  |
| `ask_off` |  |
| `audit_export_unavailable` | Available in Olivares Business: https://olivares.ai/pricing |
| `audit_failed` |  |
| `audit_filter_unavailable` |  |
| `audit_spool_full` |  |
| `authority_expired` |  |
| `authority_unavailable` |  |
| `bad_request` | Correct what the message names, then send the request again. |
| `base_not_allowed` |  |
| `binding_not_approved` |  |
| `business_required` | Available in Olivares Business: https://olivares.ai/pricing |
| `caep_requires_business` | Available in Olivares Business: https://olivares.ai/pricing |
| `cannot_remove_self` | Ask another administrator to remove your account. |
| `checkpoint_files_gone` |  |
| `checkpoint_not_found` |  |
| `commit_outcome_unknown` |  |
| `commit_refused` |  |
| `compliance_views_unavailable` |  |
| `conflict` |  |
| `connector_onboarding_unavailable` |  |
| `connector_test_failed` |  |
| `connector_unavailable` | Available in Olivares Business: https://olivares.ai/pricing |
| `container_image_missing` |  |
| `content_mismatch` |  |
| `coordination_unavailable` |  |
| `cross_tenant_admin_pool_not_configured` |  |
| `departments_unavailable` | Available in Olivares Business: https://olivares.ai/pricing |
| `diff_too_large` |  |
| `diff_unreadable` |  |
| `dispatch_deadline_passed` |  |
| `domain_claimed` | Remove the domain from the other identity provider first, or use another domain. |
| `domain_claimed_elsewhere` |  |
| `dr_unavailable` |  |
| `edition_required` | Add a license in Settings > Edition, or see https://olivares.ai/pricing |
| `entity_authorization_unavailable` |  |
| `exposure_requires_step_up` | Confirm with a passkey or security key, or with your authenticator code when the message asks for it. Enroll either under Settings › Sign-in and security. |
| `exposure_unavailable` |  |
| `field_not_accepted` |  |
| `folder_mismatch` |  |
| `forbidden` |  |
| `git_host_diff_unavailable` |  |
| `git_host_forbidden` |  |
| `global_idp_must_be_default` | Use the alias "default" for the deployment-wide identity provider. |
| `group_cycle` | Choose a parent group that is not inside this group. |
| `group_origin_adopted` |  |
| `group_origin_read_only` |  |
| `head_unreadable` |  |
| `host_credential_refused` |  |
| `host_metrics_unavailable` |  |
| `host_unavailable` |  |
| `idempotency_key_reused` |  |
| `identity_mismatch` |  |
| `import_limit` |  |
| `install_busy` |  |
| `integrity_violation` |  |
| `intent_settled` |  |
| `internal` |  |
| `invalid_email` | Use a full email address, such as name@example.com. |
| `invalid_request` | Correct what the message names, then send the request again. |
| `invalid_skill` |  |
| `invite_delivery_unavailable` |  |
| `invite_invalid` | Ask for a new invitation. |
| `isolation_unsupported` |  |
| `journal_full` |  |
| `last_owner` | Make another member an owner first. |
| `last_superadmin` | Enable another superadmin account first. |
| `last_webauthn_credential` |  |
| `legal_hold` |  |
| `legal_hold_unavailable` |  |
| `license_downgrade_requires_acknowledge` |  |
| `license_invalid` | Use the whole license file exactly as you received it. |
| `license_managed_externally` | Change the license where the engine starts (--license, OLIVARES_LICENSE_PATH or OLIVARES_LICENSE), or remove that setting. |
| `license_unavailable` |  |
| `link_not_allowed` |  |
| `locked_out` | Wait 15 minutes, then try again. |
| `log_broker_unavailable` |  |
| `login_enforcement_unavailable` |  |
| `mcp_gateway_file_owned` |  |
| `mcp_gateway_invalid` | Correct what the message names, then send the request again. |
| `mcp_gateway_name_taken` |  |
| `mcp_gateway_unavailable` |  |
| `method_not_allowed` |  |
| `module_error` |  |
| `module_not_enabled` |  |
| `modules_not_recorded` |  |
| `modules_restart_unavailable` |  |
| `modules_unavailable` |  |
| `multi_idp_requires_enterprise` | Available in Olivares Business: https://olivares.ai/pricing |
| `network_not_allowed` |  |
| `no_branch` |  |
| `no_commit` |  |
| `no_webauthn_credential` | Register a passkey under Settings › Sign-in and security. |
| `node_unavailable` |  |
| `not_a_git_repository` |  |
| `not_forgotten` |  |
| `not_found` |  |
| `not_leader` |  |
| `not_running` |  |
| `not_started` |  |
| `not_stopped` |  |
| `not_superadmin` |  |
| `not_tenant_governed` |  |
| `not_waiting` |  |
| `nothing_to_run_on` |  |
| `nothing_to_ship` |  |
| `notify_destination_test_failed` |  |
| `notify_destinations_unavailable` |  |
| `observability_export_unavailable` | Available in Olivares Business: https://olivares.ai/pricing |
| `ollama_error` |  |
| `operation_conflict` |  |
| `organizations_unavailable` | Available in Olivares Enterprise: https://olivares.ai/pricing |
| `os_account_conflict` |  |
| `os_account_refused` |  |
| `os_account_unavailable` |  |
| `passkey_not_enrolled` |  |
| `pin_drift_changed` |  |
| `pin_version_conflict` |  |
| `piv_not_configured` |  |
| `piv_verification_failed` |  |
| `plan_changed` |  |
| `plan_expired` |  |
| `posture_export_unavailable` | Available in Olivares Business: https://olivares.ai/pricing |
| `provider_default_model_unavailable` |  |
| `provider_sync_failed` |  |
| `push_prefix_overlaps_base` |  |
| `rate_limited` | Wait a moment, then try again. |
| `read_only` |  |
| `recording_consent_required` | Acknowledge the recording notice, then try again. |
| `recording_unavailable` |  |
| `recovery_required` |  |
| `ref_not_allowed` |  |
| `repository_config_refused` |  |
| `repository_outside_binding` |  |
| `request_abandoned` |  |
| `request_body_too_large` | Send a smaller request body. |
| `residency_violation` |  |
| `restore_restart_required` |  |
| `retention_period` |  |
| `retirement_pending` |  |
| `rights_data_stale` |  |
| `role_ceiling` | Ask someone whose role is at least the one you are granting to make this change. |
| `route_decision_unavailable` |  |
| `rtbf_coordinator` |  |
| `runtime_credential_refused` |  |
| `scope_active_idp_exists` | Deactivate the current identity provider first. |
| `scoped_grant_required` |  |
| `secret_scan_unavailable` |  |
| `secret_store_unavailable` |  |
| `separation_of_duty` |  |
| `session_idle_settings_unavailable` |  |
| `session_scope_mismatch` |  |
| `session_source_refused` |  |
| `session_source_unavailable` |  |
| `setup_complete` | This engine is already set up: sign in with one of its accounts. |
| `setup_required` | Finish setup first: open the console and enter the one-time setup token the engine printed at its first start. |
| `setup_token_unreadable` |  |
| `skill_name_conflict` |  |
| `source_changed` |  |
| `source_roster_unavailable` |  |
| `source_unavailable` |  |
| `sso_builder_unavailable` |  |
| `sso_group_reconcile_failed` |  |
| `sso_groups_indeterminate` |  |
| `sso_not_configured` |  |
| `sso_provider_unavailable` |  |
| `sso_required` | Use the “Sign in with” button of your identity provider on the console's sign-in page. |
| `sso_unavailable` |  |
| `step_up_required` | Confirm with a passkey or security key, or with your authenticator code when the message asks for it. Enroll either under Settings › Sign-in and security. |
| `store_unavailable` |  |
| `subject_erased` |  |
| `subject_erasing` |  |
| `subject_mismatch` |  |
| `subject_retired` |  |
| `subject_retirement_active` |  |
| `target_authority_unavailable` |  |
| `target_changed` |  |
| `target_in_use` |  |
| `tenant_not_in_service` |  |
| `tenant_suspended` |  |
| `tool_in_use` |  |
| `tool_not_installed` |  |
| `tool_pin_failed` |  |
| `tool_signin_unreadable` |  |
| `totp_not_enrolled` | Set up an authenticator app under Settings › Sign-in and security. |
| `totp_required` | Sign in again and finish the authenticator step: enter its code, or set it up when asked. |
| `totp_sign_in_required` |  |
| `totp_unavailable` |  |
| `tracing_unavailable` |  |
| `unauthenticated` | Sign in again, or send a valid API token. |
| `unavailable` |  |
| `unknown_effect` |  |
| `unknown_module` | List the module names with `olivares modules ls`. |
| `unknown_ref` |  |
| `unresolved_intent` |  |
| `unsafe_pack` |  |
| `unsupported_requirement` |  |
| `unsupported_source` |  |
| `up_to_date` |  |
| `update_refused` |  |
| `update_restart_unavailable` |  |
| `update_unsupported` |  |
| `updates_unavailable` |  |
| `upstream` |  |
| `user_cap_requires_enterprise` |  |
| `vault_no_totp` |  |
| `version_conflict` |  |
| `weak_password` | Choose a longer password. |
| `webauthn_relying_party_unusable` |  |
| `webauthn_verification_failed` |  |
| `workflow_change_refused` |  |
| `workspace_confined` | Ask an administrator for access to that workspace. |
| `workspace_confined_account_writer` | Ask an administrator to create the account. |
| `workspace_contents_unavailable` |  |
| `workspace_cycle` | Choose a parent that is not inside this department. |

<!-- END GENERATED olivares-api-error-codes -->
