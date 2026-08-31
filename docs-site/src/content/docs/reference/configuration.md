---
title: "Configuration reference"
description: "The verified configuration surface of the Olivares AI control plane: serve flags, environment variables, store selection, and the secure defaults that ship out of the box."
---

This page documents the configuration surface of the control plane engine — the single Go binary named `olivares`. It covers the flags accepted by the `serve` subcommand, the environment variables the engine reads at boot, how the store and policy decision point are selected, and the secure defaults that are in effect with no configuration at all.

Everything listed here is taken from the engine's own command definitions and composition root. Where a setting cannot be confirmed in source it is not listed. For the conceptual security posture behind these defaults, see [the security model](/explanation/security/security-model/); for the runnable end-to-end path, see [self-hosting](/how-to/self-hosting/).

:::note[Configuration philosophy]
The engine is configured by flags and by environment variables, not by a sprawling config file. Every variable it reads is listed below, generated from the sources themselves. Secrets that wire real sources stay in operator-held files referenced by environment variable — never in the store. The defaults are chosen to fail closed: loopback binds, TLS on, no default credentials.
:::

## The `serve` subcommand

`olivares serve` runs the REST/web HTTP server and the gRPC server in one process, with the web UI served from the same origin as the API. The flags below are the verified configuration inputs to that command.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8443` | HTTP listen address (REST API + embedded web UI). |
| `--grpc-listen` | `127.0.0.1:8444` | gRPC listen address (control-plane / collector ingest API). |
| `--data-dir` | `$OLIVARES_DATA_DIR`, an existing `./olivares-data`, else `$XDG_DATA_HOME/olivares` or `~/.local/share/olivares` | Data directory: audit signing key, TLS material, and (for SQLite) the store file. |
| `--engine` | `sqlite` | Store engine: `sqlite` or `postgres`. |
| `--dsn` | empty (SQLite file in the data dir) | Store connection string. |
| `--checkpoint-interval` | `1h` | How often a signed audit checkpoint is written over every tenant chain. `0` disables. |
| `--insecure` | off | Serve plaintext HTTP/gRPC. Dangerous; localhost development only. |
| `--seed-demo` | off | Load a synthetic sample estate for demos/E2E. Refuses to start on a non-loopback bind. |

TLS is on by default. With no `--tls-cert`/`--tls-key` supplied, the engine ensures a self-signed certificate in the data directory once, up front, before any listener accepts a connection, so both the HTTP and gRPC servers use the same certificate and neither falls back to plaintext. When it generates a self-signed certificate it logs `cert_fingerprint_sha256` (the certificate digest, what a browser shows) and `pin_sha256` (the leaf SPKI digest). `--pin-sha256` takes the second one, in base64 or hex; the certificate fingerprint is a different digest — it parses, 32 bytes either way, and then fails at the handshake with `TLS SPKI pin mismatch`, which reports the value to use.

:::caution[`--insecure` is loopback-only by intent]
`--insecure` serves plaintext HTTP and gRPC, which would expose bearer tokens on the wire. The gRPC path **fails closed**: outside `--insecure` the server refuses to construct a plaintext listener rather than degrade silently. Use `--insecure` only against `127.0.0.1` during local development, never on a published address.
:::

:::danger[`--seed-demo` is synthetic and self-protecting]
`--seed-demo` provisions a demo administrator with a **public, source-tree password** and fabricated estate data. It is for demos and E2E only. The engine refuses to start it on a non-loopback listener: if either `--listen` or `--grpc-listen` is not a loopback address the command exits with an error. Use a throwaway data directory; never point it at real data.
:::

A full flag listing — including the Postgres-only and mutual-TLS flags used in distributed deployments — is in the [CLI reference](/reference/cli/). This page documents the common configuration surface; some advanced flags govern multi-node topologies described in [the architecture overview](/explanation/architecture/overview/).

## Environment variables

The three groups below are the ones an operator meets first, written out with their behaviour. The complete roster follows them, generated from the engine's own sources so it cannot fall behind the binary.

### Data directory

| Variable | Effect |
| --- | --- |
| `OLIVARES_DATA_DIR` | Default data directory when `--data-dir` is not given. Without it the engine uses an existing `./olivares-data` installation, else `$XDG_DATA_HOME/olivares` or `~/.local/share/olivares` — never the current working directory, which is where it would leave private keys. |

The data directory holds the audit signing key, the TLS certificate and key, and — for the SQLite engine — the store file. Persist it across restarts.

### Wiring real sources

| Variable | Effect |
| --- | --- |
| `OLIVARES_SOURCES_CONFIG` | Path to a JSON file that wires real observation sources and identity roster providers before the engine starts. |

`OLIVARES_SOURCES_CONFIG` is the single input through which non-demo signal sources and identity roster providers are resolved. It is the operator's secret-bearing configuration and is deliberately kept out of the store. The engine reads it during boot and registers every source **before** the runtime starts.

The handling is honest rather than fail-fast:

- A **missing** variable yields an empty configuration, and the engine warns that nothing real is wired.
- An **unreadable or invalid-JSON** file warns and yields an empty configuration — it never aborts the boot.
- A configured-but-**empty** source list warns that no connector will ingest, so the estate runs on no live traffic, rather than silently appearing healthy.
- An empty **identity** list warns that the roster stays empty and roster sync is a no-op.

This is by design: an unconfigured source surfaces a warning instead of crashing the control plane or pretending to work. To actually populate the access map, configure at least one source — see [connect a source](/how-to/connect-a-source/) and, for the cooperative Claude Code path over OpenTelemetry and MCP, [connect Claude Code](/how-to/connect-claude-code/).

### Authorization decision point (PDP)

The authorization policy decision point is selected at the composition root by environment. The native attribute-based access control (ABAC) engine and role-based access control (RBAC) always govern; the external PDP, when selected, is an additional **restrict-only** layer that can never widen access.

| Variable | Effect |
| --- | --- |
| `OLIVARES_PDP_ENGINE` | Selects the external PDP: `cedar`, `opa`, or `none` (empty or `none` means native ABAC only). |
| `OLIVARES_PDP_CEDAR_FILE` | Cedar engine only: path to the operator's Cedar policy file. |
| `OLIVARES_PDP_OPA_URL` | OPA engine only: base URL of the Open Policy Agent endpoint. |
| `OLIVARES_PDP_OPA_PATH` | OPA engine only: decision path queried under that endpoint. |
| `OLIVARES_PDP_OPA_TOKEN` | OPA engine only: bearer token for the OPA endpoint. |

Two adapters sit behind one seam: an **embedded Cedar** evaluator (the primary, pure-Go path) and an **OPA-over-HTTP** adapter. The operator chooses one engine; both can only restrict, never widen, the decision the built-in RBAC already made.

:::note[A bad policy never un-governs the plane]
If `OLIVARES_PDP_ENGINE` selects an engine but its configuration is invalid — an unreadable Cedar file, a malformed OPA target — the engine **disables only the external PDP**, keeps the native ABAC engine and RBAC enforcing, and logs loudly. A broken policy file never silently leaves requests un-governed and never crashes the control plane.
:::

For the deny-by-default model, the privileged nature of viewing the access graph, and how every authorization read is audited, see [the security model](/explanation/security/security-model/).

<!-- BEGIN GENERATED olivares-env-reference — regenerate with `bash scripts/check-config-env-docs.sh --write`; do not edit by hand -->

### Complete variable reference

The table below is generated from the product's own sources: 266 variables and 17 runtime-constructed families, covering the engine, the CLI, the Kubernetes operator, the Terraform provider and the connectors. It is regenerated and checked against those sources on every change, so it does not fall behind the binary.

**Required** means the feature that reads the variable does not start without it; most variables are optional and the engine runs with none of them set.

| Variable | Required | Default | What it configures |
| --- | --- | --- | --- |
| `OLIVARES_ACTOR` | No | — | Default `--actor` for the decision-bearing eventing verbs, so a scripted change still records who made it. |
| `OLIVARES_ADMIN_DSN` | No | — | Privileged connection string the Kubernetes operator uses for schema migration, separate from the least-privilege runtime role. |
| `OLIVARES_AGENTCORE_EXPORT_CONFIG` | No | — | Path to the JSON configuration of the AgentCore usage export. |
| `OLIVARES_AGENT_GATEWAY_CONFIG` | No | — | Path to the JSON configuration of the MCP agent gateway. |
| `OLIVARES_ALLOW_CLEARTEXT` | No | — | Dangerous opt-in: lets a request carrying a credential reach a NON-loopback host over plain HTTP, for surfaces with no --allow-cleartext flag of their own. |
| `OLIVARES_API_TOKEN` | No | — | API token the Terraform provider authenticates with, when the provider block does not set one. |
| `OLIVARES_APPROVAL_BRIDGE_CONFIG` | No | — | Path to the JSON configuration of the bridge that routes approvals to an external system. |
| `OLIVARES_AUDIT_ARCHIVE_CONFIG` | No | — | Path to the JSON settings for the `s3archive` sink. Secret-bearing, so it is a file rather than a value. |
| `OLIVARES_AUDIT_ARCHIVE_DIR` | No | — | Root directory for the `dir` archive sink. |
| `OLIVARES_AUDIT_ARCHIVE_INTERVAL` | No | `24h` | How often sealed audit segments are archived, as a Go duration. |
| `OLIVARES_AUDIT_ARCHIVE_RETAIN_DAYS` | No | `2555` | How long archived audit segments are retained, in days. |
| `OLIVARES_AUDIT_ARCHIVE_SEGMENT_EVENTS` | No | — | How many events a sealed archive segment holds before the next one is started. |
| `OLIVARES_AUDIT_ARCHIVE_SINK` | No | — | Where sealed audit segments are archived: unset for off, `dir` for a local directory, `s3archive` for object storage. |
| `OLIVARES_AUDIT_LEGALHOLD_INTERVAL` | No | — | How often the long-horizon legal-hold sweep runs, as a Go duration. |
| `OLIVARES_AUDIT_META_BLINDING` | No | — | Whether audit metadata commitments are written blinded, and how strictly that is required. |
| `OLIVARES_AUDIT_SIGNING_KEY` | No | — | Audit checkpoint signing key, inline. Prefer the file form so the key never sits in a process environment. |
| `OLIVARES_AUDIT_SIGNING_KEY_FILE` | No | — | Path to the audit checkpoint signing key. This is the operator-held form. |
| `OLIVARES_AUDIT_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the audit signing key wrapped by a key management service, unwrapped at boot. |
| `OLIVARES_AUDIT_SPOOL_MAX_BYTES` | No | — | Upper bound on the on-disk audit spool before the full-spool rule applies. |
| `OLIVARES_AUDIT_SPOOL_ON_FULL` | No | — | What happens when the audit spool is full: the deny-closed posture refuses the write rather than dropping the record. |
| `OLIVARES_AUTHZEN_ALLOWED_CIDRS` | No | — | Comma-separated CIDR ranges allowed to reach the AuthZEN endpoints. Unset leaves the endpoint reachable wherever the listener is. |
| `OLIVARES_AUTHZEN_DISABLED` | No | — | Set to a true value to turn the AuthZEN decision endpoint off. |
| `OLIVARES_AUTHZEN_EXPORT_DISABLED` | No | — | Set to a true value to turn the AuthZEN export endpoint off. |
| `OLIVARES_AUTHZEN_SEARCH_DISABLED` | No | — | Set to a true value to turn the AuthZEN search endpoints off while leaving decisions on. |
| `OLIVARES_BASE_URL` | No | — | Public base URL of this control plane, used where an absolute link back to it has to be produced. |
| `OLIVARES_BUS_CONFIG` | No | — | Path to the JSON configuration of the message bus the engine publishes on. |
| `OLIVARES_CAEP_TRANSMITTER_CONFIG` | No | — | Path to the JSON configuration of the CAEP transmitter that pushes shared-signal events. |
| `OLIVARES_CATALOG_SIGNING_KEY` | No | — | Catalog signing key, inline. Prefer the file form. |
| `OLIVARES_CATALOG_SIGNING_KEY_FILE` | No | — | Path to the catalog signing key. |
| `OLIVARES_CATALOG_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the catalog signing key wrapped by a key management service. |
| `OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG` | No | — | Path to the JSON configuration of the administrative actuator that applies changes at the model provider. |
| `OLIVARES_CLAUDE_ADMIN_KEY` | No | — | Administrative API key used to read identity posture from the model provider. |
| `OLIVARES_CLAUDE_ERASER_CONFIG` | No | — | Path to the JSON configuration of the erasure actuator that carries out deletion requests. |
| `OLIVARES_CLAUDE_FILES_CONFIG` | No | — | Path to the JSON configuration of the provider file inventory scan. |
| `OLIVARES_CLAUDE_INFERENCE_KEY` | No | — | API key the engine uses for its own inference calls. Unset leaves the inference-backed features off. |
| `OLIVARES_CLAUDE_WORKSPACE_ID` | No | — | Workspace whose identity posture is read, when the administrative key spans several. |
| `OLIVARES_CLI_CONFIG` | No | — | Path to the CLI configuration file, replacing the default per-user location. Used by hermetic automation. |
| `OLIVARES_CLI_TRAMPOLINE` | No | — | Set to `1` inside a re-executed child process so the binary runs the requested subcommand instead of the outer test harness. |
| `OLIVARES_CODEX_HOOK_ACCOUNT` | No | — | Account the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_AGENT` | No | — | Agent identity the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_ORG` | No | — | Organization the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the Codex hook enforcement point server. |
| `OLIVARES_CODEX_HOOK_TENANT` | No | — | Tenant the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_TOKEN` | No | — | Token the Codex hook client presents to the enforcement point. |
| `OLIVARES_CODEX_HOOK_URL` | No | — | Base URL of the enforcement point the Codex hook client calls. |
| `OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE` | Yes | — | Path to the JSON keyring the communication content sealer loads at boot (cmd/olivares/boot.go). Secret-bearing, so it is a file rather than a value: sealed message bodies are verified against the keys it carries, and an engine started without it cannot open content sealed by a peer that had one. |
| `OLIVARES_COMMUNICATION_TOKEN` | Yes | — | NOT an operator setting, and documented here precisely so nobody sets it. The engine MINTS this bearer and injects it into a conducted session's child process exactly once (modules/sessions/runtime_bridge.go); its tuple travels inside the authenticated principal. It is RESERVED on the launch path: validateLaunchInjectedEnv (modules/sessions/runtime.go) refuses any launch whose injected environment carries it, so a caller-supplied value is rejected rather than honoured. It appears in the roster because that reserved-name check mentions it, not because the engine reads it. |
| `OLIVARES_CONFIG_STRICT` | No | — | Set to `1` to make `olivares config effective` and `config validate` reject any unrecognized `OLIVARES_*` key. |
| `OLIVARES_CONTEXT_MAX_TOKENS` | No | — | Upper bound on the context a governed session may assemble, in tokens. |
| `OLIVARES_CONTEXT_STRATEGY` | No | — | Which strategy assembles a governed session's context when the bound is reached. |
| `OLIVARES_DATA_DIR` | No | — | Data directory used when `--data-dir` is not given: audit signing key, TLS material and, for SQLite, the store file. |
| `OLIVARES_DB_MAX_CONNS` | No | — | Upper bound on pooled database connections. Unset leaves the driver default. |
| `OLIVARES_DEPLOY_EXECUTOR_CONFIG` | No | — | Path to the JSON configuration of the executor that applies deployment changes. |
| `OLIVARES_DR_KEK_FILE` | No | — | Path to a raw 32-byte key-encryption key for backups, for the path where a key management service does the unwrapping. |
| `OLIVARES_DR_OFFSITE_ACCESS_KEY_ID_FILE` | No | — | Path to the file holding the offsite access key id, so the credential stays out of the environment. |
| `OLIVARES_DR_OFFSITE_BUCKET` | No | — | Offsite bucket for disaster-recovery bundles. Setting it turns offsite replication on. |
| `OLIVARES_DR_OFFSITE_ENDPOINT` | No | — | S3-compatible endpoint for offsite replication. Unset means AWS S3 in the configured region. |
| `OLIVARES_DR_OFFSITE_PREFIX` | No | — | Key prefix for bundles inside the offsite bucket. |
| `OLIVARES_DR_OFFSITE_REGION` | No | — | Region for offsite replication. |
| `OLIVARES_DR_OFFSITE_SECRET_ACCESS_KEY_FILE` | No | — | Path to the file holding the offsite secret access key. |
| `OLIVARES_DR_OFFSITE_SESSION_TOKEN_FILE` | No | — | Path to the file holding an offsite session token, for temporary credentials. |
| `OLIVARES_DR_PASSPHRASE_FILE` | No | — | Path to the backup passphrase file, from which the backup key-encryption key is derived. |
| `OLIVARES_DR_SCHEDULE_INTERVAL` | No | — | How often the scheduled backup runs, as a Go duration. |
| `OLIVARES_DSN` | No | — | Store connection string injected by the Kubernetes operator into the engine it manages. |
| `OLIVARES_DURABLE_BUS_CONFIG` | No | — | Path to the JSON configuration of the durable bus, for at-least-once delivery across replicas. |
| `OLIVARES_EMBEDDINGS_BASE_URL` | No | — | Endpoint the openai-compatible embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_DIM` | No | — | Vector dimension the openai-compatible provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_GEO` | No | — | Region or data-residency hint sent to the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_KEY` | No | — | Api key for the openai-compatible embeddings provider. |
| `OLIVARES_EMBEDDINGS_MODEL` | No | — | Embedding model requested from the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_BASE_URL` | No | — | Endpoint the openai embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_BASE_URL` | No | — | Endpoint the openai-compatible embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_DIM` | No | — | Vector dimension the openai-compatible provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_GEO` | No | — | Region or data-residency hint sent to the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_KEY` | No | — | Api key for the openai-compatible embeddings provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_MODEL` | No | — | Embedding model requested from the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_DIM` | No | — | Vector dimension the openai provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_OPENAI_GEO` | No | — | Region or data-residency hint sent to the openai provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_KEY` | No | — | Api key for the openai embeddings provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_MODEL` | No | — | Embedding model requested from the openai provider. |
| `OLIVARES_EMBEDDINGS_PROVIDER` | No | — | Which embeddings provider is used, pinning one instead of taking the first that is configured. |
| `OLIVARES_EMBEDDINGS_REQUIRE` | No | — | Set to a true value to make a missing or unusable embeddings provider a refusal rather than a degraded index. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_BASE_URL` | No | — | Endpoint the self-hosted embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_DIM` | No | — | Vector dimension the self-hosted provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_GEO` | No | — | Region or data-residency hint sent to the self-hosted provider. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_KEY` | No | — | Api key for the self-hosted embeddings provider. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_MODEL` | No | — | Embedding model requested from the self-hosted provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_BASE_URL` | No | — | Endpoint the voyage embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_VOYAGE_DIM` | No | — | Vector dimension the voyage provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_VOYAGE_GEO` | No | — | Region or data-residency hint sent to the voyage provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_KEY` | No | — | Api key for the voyage embeddings provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_MODEL` | No | — | Embedding model requested from the voyage provider. |
| `OLIVARES_ENDPOINT` | No | — | Control-plane base URL the Terraform provider talks to, when the provider block does not set one. |
| `OLIVARES_ENGINE` | No | — | Store engine the Kubernetes operator selects for the engine it manages: `sqlite` or `postgres`. |
| `OLIVARES_EVALS_MONITOR_WINDOW` | No | — | Time window the evaluation monitor scores, as a Go duration. |
| `OLIVARES_EVENTING_ALLOW_LOOPBACK` | No | — | Set to a true value to allow loopback destinations. Single-box development only, because the default refusal is what blocks server-side request forgery. |
| `OLIVARES_EVENTING_DISPATCH_INTERVAL` | No | `15s` | How often queued events are dispatched, as a Go duration. `0` disables the pump. |
| `OLIVARES_EVENTING_EGRESS_POLICY` | No | — | Path to the JSON policy that decides which destinations outbound events may reach. A policy that does not parse leaves eventing unwired rather than open. |
| `OLIVARES_EVENTING_RETENTION` | No | `168h` | How long delivered events are kept for replay, as a Go duration. |
| `OLIVARES_EVENTING_SECRET_KEY` | No | — | Key that encrypts eventing subscription signing secrets at rest. |
| `OLIVARES_EXTRA_ARGS` | No | — | Extra `serve` arguments appended by the packaged service unit, for operators who configure the daemon through an environment file. |
| `OLIVARES_GROK_HOOK_ACCOUNT` | No | — | Account the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_AGENT` | No | — | Agent identity the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_ORG` | No | — | Organization the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the Grok Build hook enforcement point server. Absent mounts nothing; a path given must be readable and valid or startup fails closed. |
| `OLIVARES_GROK_HOOK_TENANT` | No | — | Tenant the Grok Build hook client acts in. |
| `OLIVARES_GROK_HOOK_TOKEN` | No | — | Bearer credential the Grok Build hook client presents to the enforcement point. |
| `OLIVARES_GROK_HOOK_URL` | No | — | Endpoint of the enforcement point the Grok Build hook client calls; unset denies, deny-closed. |
| `OLIVARES_GUARDIAN_SWEEP_INTERVAL` | No | — | How often the guardian sweep runs, as a Go duration. `0` disables it. |
| `OLIVARES_HA_LEADER_GATE` | No | — | Set to `1` to make background loops run only on the elected leader, so a multi-replica deployment does not run them twice. |
| `OLIVARES_HA_LEADER_LABEL` | No | — | Label this replica publishes when it holds leadership, so an operator can route to the leader. |
| `OLIVARES_HITL_CONFIG` | No | — | Path to the JSON configuration of the human-in-the-loop review path. |
| `OLIVARES_HOOK_FIREWALL_CONFIG` | No | — | Path to the JSON configuration of the data-loss firewall that runs inside the hook. Unset leaves that half off. |
| `OLIVARES_HOOK_PEP_ACCOUNT` | No | — | Account the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_AGENT` | No | — | Agent identity the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the hook enforcement point server. |
| `OLIVARES_HOOK_PEP_ORG` | No | — | Organization the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_TENANT` | No | — | Tenant the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_TOKEN` | No | — | Token the Claude Code hook client presents to the enforcement point. |
| `OLIVARES_HOOK_PEP_URL` | No | — | Base URL of the enforcement point the Claude Code hook client calls. |
| `OLIVARES_INCIDENTLOOP_CONFIG` | No | — | Path to the JSON configuration of the incident close-the-loop subscriber. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_INFERENCE_PROXY_CONFIG` | No | — | Path to the JSON configuration of the governed inference proxy. |
| `OLIVARES_INGEST_TOKEN` | No | — | Bearer token the collector ingest endpoint requires from telemetry senders. |
| `OLIVARES_INSECURE` | No | — | Set to `1` to let the CLI talk to a plaintext or untrusted-TLS endpoint. Local development only. |
| `OLIVARES_KEY_CUSTODY` | No | — | Custody posture required of the audit signing key: whether a raw on-disk key is accepted or a wrapped one is demanded. |
| `OLIVARES_KEY_WRAP_AWS_KEY_ID` | No | — | Key identifier in AWS KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AWS_REGION` | No | — | Region of the AWS KMS key. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_KEY_NAME` | No | — | Key name in Azure Key Vault. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_KEY_VERSION` | No | — | Key version in Azure Key Vault. Unset uses the current version. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_VAULT_URL` | No | — | Azure Key Vault URL. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_KEY` | No | — | Fully qualified key version name in Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_TOKEN` | No | — | Token used against Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_TOKEN_FILE` | No | — | Path to the file holding the token used against Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_OLD` | No | — | Previous key management backend during a rewrap migration, so keys wrapped by it can still be unwrapped. |
| `OLIVARES_LEDGER_CUSTODY` | No | — | Custody posture required of the ledger checkpoint signer, the ledger counterpart of the audit key posture. |
| `OLIVARES_LEDGER_KMS_AWS_KEY_ID` | No | — | Key identifier in AWS KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AWS_REGION` | No | — | Region of the AWS KMS key. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AWS_SIGNING_ALG` | No | — | Signing algorithm requested from AWS KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_KEY_NAME` | No | — | Key name in Azure Key Vault. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_KEY_VERSION` | No | — | Key version in Azure Key Vault. Unset uses the current version. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_VAULT_URL` | No | — | Azure Key Vault URL. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_KEY` | No | — | Fully qualified key version name in Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_TOKEN` | No | — | Token used against Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_TOKEN_FILE` | No | — | Path to the file holding the token used against Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_SIGNER` | No | — | Off-box checkpoint signer to use: which key management backend signs audit checkpoints instead of a local key. |
| `OLIVARES_LICENSE` | No | — | License document itself, inline, for deployments that cannot mount a file. |
| `OLIVARES_LICENSE_PATH` | No | — | Path to the license document on disk. Takes effect before the inline form. |
| `OLIVARES_LICENSE_PUBKEY` | No | — | Public key the engine verifies the license signature against. |
| `OLIVARES_LIVEINGEST_INSPECT_OBSERVED_REFS` | No | — | Set to `1` to make live ingest inspect observed references, which costs more per event. |
| `OLIVARES_LOG_LEVEL` | No | — | Minimum log level the engine emits: `debug`, `info`, `warn` or `error`. |
| `OLIVARES_MCP_TASK_KILLSWITCH_SWEEP` | No | — | How often a running MCP task is re-checked against the kill switch, as a Go duration. |
| `OLIVARES_METRICS_ALLOWED_CIDRS` | No | — | Comma-separated CIDR ranges allowed to scrape the metrics endpoint. |
| `OLIVARES_METRICS_TOKEN` | No | — | Bearer token the metrics endpoint requires. Unset leaves the endpoint unauthenticated behind whatever the listener exposes. |
| `OLIVARES_NHI_ACTUATORS_CONFIG` | No | — | Path to the JSON configuration of the actuators that act on non-human identities. |
| `OLIVARES_NIS2INCIDENT_CONFIG` | No | — | Path to the JSON configuration of NIS2 incident reporting. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_NOTIFY_CONFIG` | No | — | Path to the JSON list of notification destinations. Secret-bearing, so it stays out of the store. |
| `OLIVARES_NOTIFY_DISPATCH_INTERVAL` | No | — | How often queued notifications are dispatched, as a Go duration. `0` disables the pump. |
| `OLIVARES_OIDC_CLIENT_ID` | Yes | — | OIDC client id for this control plane. Required when the protocol is `oidc`. |
| `OLIVARES_OIDC_CLIENT_SECRET` | Yes | — | OIDC client secret for this control plane. Required when the protocol is `oidc`. |
| `OLIVARES_OIDC_GROUPS_CLAIM` | No | — | ID-token or UserInfo claim carrying group membership. Unset leaves group mapping off. |
| `OLIVARES_OIDC_ISSUER` | Yes | — | OIDC issuer URL. Required when the protocol is `oidc`. |
| `OLIVARES_ORCH_CADENCE_INTERVAL` | No | — | How often the orchestration cadence loop runs, as a Go duration. `0` disables it. |
| `OLIVARES_ORCH_DISPATCH_CONFIG` | No | — | Path to the JSON configuration for orchestration dispatch targets. |
| `OLIVARES_ORCH_WORKFLOW_INTERVAL` | No | `15s` | How often the orchestration workflow loop advances waiting runs, as a Go duration. |
| `OLIVARES_ORCH_WORKFLOW_MAX` | No | — | Upper bound on concurrently advancing workflow runs. |
| `OLIVARES_ORCH_WORKFLOW_STEPS_MAX` | No | — | Upper bound on the steps one workflow run may take, which stops a loop from running forever. |
| `OLIVARES_OTA_PUBKEY` | No | — | Public key the engine verifies a downloaded update bundle against. |
| `OLIVARES_OTEL_ENABLED` | No | — | Set to a true value to export traces. Setting an endpoint turns export on as well. |
| `OLIVARES_OTEL_ENDPOINT` | No | — | OTLP endpoint traces are exported to. Falls back to the standard `OTEL_EXPORTER_OTLP_ENDPOINT`. |
| `OLIVARES_OTEL_GENAI_COMPAT` | No | — | Set to a true value to also emit the generative-AI semantic-convention attributes on spans. |
| `OLIVARES_OTEL_INSECURE` | No | — | Set to a true value to export traces over plaintext. Local development only. |
| `OLIVARES_OTEL_PROTOCOL` | No | — | OTLP protocol used for export. Falls back to the standard `OTEL_EXPORTER_OTLP_PROTOCOL`. |
| `OLIVARES_OTEL_SAMPLE_RATIO` | No | — | Fraction of traces sampled, between 0 and 1. |
| `OLIVARES_OTEL_SERVICE_NAME` | No | — | Service name reported on exported traces. |
| `OLIVARES_PDP_CEDAR_FILE` | No | — | Path to the Cedar policy file, for the `cedar` decision point. |
| `OLIVARES_PDP_ENGINE` | No | — | External policy decision point to add on top of the native engine: `cedar`, `opa` or `none`. |
| `OLIVARES_PDP_OPA_PATH` | No | — | Decision path queried under the Open Policy Agent endpoint. |
| `OLIVARES_PDP_OPA_TOKEN` | No | — | Bearer token for the Open Policy Agent endpoint. |
| `OLIVARES_PDP_OPA_URL` | No | — | Base URL of the Open Policy Agent endpoint, for the `opa` decision point. |
| `OLIVARES_PIV_CONFIG` | No | — | Path to the JSON configuration for smart-card privileged login. |
| `OLIVARES_PLUGIN` | No | — | Handshake cookie an out-of-process connector plugin must present. Set by the engine when it launches the plugin, not by the operator. |
| `OLIVARES_POLICY_MAX_STALENESS` | No | — | How stale a cached policy decision may be before it is refused, as a Go duration. |
| `OLIVARES_POLICY_SIGNING_KEY` | No | — | Policy bundle signing key, inline. Prefer the file form. |
| `OLIVARES_POLICY_SIGNING_KEY_FILE` | No | — | Path to the policy bundle signing key. |
| `OLIVARES_POLICY_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the policy signing key wrapped by a key management service. |
| `OLIVARES_RATELIMIT_CONFIG` | No | — | Path to the JSON rate-limit policy the engine applies to its own endpoints. |
| `OLIVARES_RATELIMIT_STORE` | No | — | Where rate-limit counters live, which decides whether limits are per replica or shared. |
| `OLIVARES_REPORTING_CONFIG` | No | — | Path to the JSON configuration of the reporting add-on. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_REPORTING_SCHEDULE_INTERVAL` | No | — | How often scheduled reports are generated, as a Go duration. |
| `OLIVARES_REPORT_CACHE_DIR` | No | — | Directory where generated report artifacts are cached. |
| `OLIVARES_RETENTION_SWEEP_INTERVAL` | No | — | How often the retention sweep deletes data past its retention window, as a Go duration. |
| `OLIVARES_SAML_ACS_URL` | Yes | — | Assertion consumer service URL of this service provider, where the identity provider posts the assertion. |
| `OLIVARES_SAML_EMAIL_ATTRIBUTE` | No | — | Assertion attribute carrying the user's email. Unset tries the common attribute names. |
| `OLIVARES_SAML_GROUPS_ATTRIBUTE` | No | — | Multi-valued assertion attribute carrying group membership. Unset leaves group mapping off. |
| `OLIVARES_SAML_IDP_METADATA_URL` | No | — | Identity-provider metadata URL, from which the SAML endpoints and certificate are read. |
| `OLIVARES_SAML_IDP_SSO_URL` | No | — | Identity-provider single sign-on URL, for the path where metadata is not fetched. |
| `OLIVARES_SAML_SP_CERT_PEM` | No | — | Service-provider encryption certificate in PEM, published as the encryption key descriptor. |
| `OLIVARES_SAML_SP_ENTITY_ID` | Yes | — | Entity id this control plane presents as the SAML service provider. Required when the protocol is `saml`. |
| `OLIVARES_SAML_SP_KEY_PEM` | No | — | Service-provider encryption private key in PEM, which decrypts encrypted assertions. |
| `OLIVARES_SAML_SP_SIGN_CERT_PEM` | No | — | Service-provider signing certificate in PEM, published as the signing key descriptor. |
| `OLIVARES_SAML_SP_SIGN_KEY_PEM` | No | — | Service-provider signing private key in PEM, which signs authentication requests. |
| `OLIVARES_SANDBOX_RUNTIME_CONFIG` | No | — | Path to the JSON configuration of the sandbox runtime that isolates agent execution. |
| `OLIVARES_SECRETREF_AWS_REGION` | No | — | Region used for AWS Secrets Manager references. Falls back to `AWS_REGION` and `AWS_DEFAULT_REGION`. |
| `OLIVARES_SECRETREF_AZURE_API_VERSION` | No | — | Azure Key Vault API version requested. |
| `OLIVARES_SECRETREF_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. |
| `OLIVARES_SECRETREF_AZURE_VAULT_URL` | No | — | Default Azure Key Vault URL for references that do not name one. |
| `OLIVARES_SECRETREF_GCP_ENDPOINT` | No | — | Endpoint override for Google Secret Manager. |
| `OLIVARES_SECRETREF_GCP_PROJECT` | No | — | Default Google Cloud project for Secret Manager references that do not name one. |
| `OLIVARES_SECRETREF_GCP_TOKEN` | No | — | Token used against Google Secret Manager. |
| `OLIVARES_SECRETREF_INFISICAL_ENV` | No | — | Default Infisical environment for references that do not name one. |
| `OLIVARES_SECRETREF_INFISICAL_TOKEN` | No | — | Token used against Infisical. |
| `OLIVARES_SECRETREF_INFISICAL_URL` | No | — | Base URL of the Infisical server secret references resolve against. |
| `OLIVARES_SECRETREF_INFISICAL_WORKSPACE_ID` | No | — | Default Infisical workspace for references that do not name one. |
| `OLIVARES_SECRETREF_K8S_APISERVER` | No | — | Kubernetes API server secret references resolve against. |
| `OLIVARES_SECRETREF_K8S_CA_FILE` | No | — | Path to the certificate authority bundle used to verify the Kubernetes API. |
| `OLIVARES_SECRETREF_K8S_TOKEN_FILE` | No | — | Path to the service-account token file used against the Kubernetes API. |
| `OLIVARES_SECRETREF_VAULT_ADDR` | No | — | Address of the HashiCorp Vault server secret references resolve against. Falls back to `VAULT_ADDR`. |
| `OLIVARES_SECRETREF_VAULT_NAMESPACE` | No | — | Vault namespace secret references resolve in. Falls back to `VAULT_NAMESPACE`. |
| `OLIVARES_SECRETREF_VAULT_TOKEN` | No | — | Token used against HashiCorp Vault. |
| `OLIVARES_SECRET_STORE_KEY` | No | — | Key that encrypts operator secrets held in the store. |
| `OLIVARES_SERVER_URL` | No | — | Base URL of the control plane the CLI talks to, when `--server` is not given. |
| `OLIVARES_SESSION_BUDGET_AVAILABILITY` | No | — | Whether session budget enforcement is required, and what happens when the budget service cannot answer. |
| `OLIVARES_SESSION_CONTEXT_AVAILABILITY` | No | — | Whether session context governance is required, and what happens when the context service cannot answer. |
| `OLIVARES_SESSION_KILLSWITCH_SWEEP` | No | `15s` | How often an active session is re-checked against the kill switch, as a Go duration. `0` leaves only the check at launch. |
| `OLIVARES_SESSION_PEP_TOKEN_FILE` | No | — | Path to the file holding the token the session enforcement point requires. |
| `OLIVARES_SESSION_PEP_URL` | No | — | Base URL of the policy enforcement point a governed agent session calls before acting. |
| `OLIVARES_SESSION_RUNTIME_BASE_URL` | No | — | Base URL the launched session runtime calls back to. |
| `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` | No | `claude` | Executable the session runtime launches. |
| `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` | No | — | Path to the file holding the session runtime's credential, refreshed by rotation. |
| `OLIVARES_SESSION_RUNTIME_TOKEN_TTL` | No | `15m` | Lifetime of a minted session runtime credential, as a Go duration. |
| `OLIVARES_SESSION_RUNTIME_WIF` | No | — | Whether the session runtime takes its credential from workload identity federation instead of a token file. |
| `OLIVARES_SESSION_RUNTIME_WIF_RULE` | No | — | Which federation rule the session runtime exchanges its workload identity under. |
| `OLIVARES_SIEM_FORWARD_INTERVAL` | No | — | How often signed ledger records are forwarded to the configured SIEM, as a Go duration. |
| `OLIVARES_SOURCES_CONFIG` | No | — | Path to the JSON file that wires real observation sources and identity roster providers before the engine starts. |
| `OLIVARES_SSO_PROTOCOL` | No | — | Single sign-on protocol to wire: `oidc` or `saml`. Unset means no federation, and the endpoints report it rather than half-wiring one. |
| `OLIVARES_SSO_SECRET_KEY` | No | — | Key that encrypts the federation client secret and service-provider private keys at rest. |
| `OLIVARES_TARGET_BINDING_KEY` | No | — | Key that binds an orchestration target to this deployment, inline. Prefer the file form. |
| `OLIVARES_TARGET_BINDING_KEY_FILE` | No | — | Path to the orchestration target binding key. |
| `OLIVARES_TENANT` | No | — | Default tenant for CLI commands, when `--tenant` is not given. |
| `OLIVARES_THREATINTEL_CONFIG` | No | — | Path to the JSON configuration of threat-intelligence ingest. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_THREATINTEL_SIGNING_KEY` | No | — | Signing key for threat-intelligence bundles the engine publishes. |
| `OLIVARES_TOKEN` | No | — | API token the CLI authenticates with, when `--token` is not given. |
| `OLIVARES_UPDATE_CHANNEL` | No | — | Release channel the update check asks for, such as `stable`. |
| `OLIVARES_UPDATE_ENDPOINT` | No | — | Base URL the update check queries. Unset leaves the update check off. |
| `OLIVARES_UPGRADE_TOKEN` | No | — | Download token `olivares upgrade` presents when fetching a build from a credentialed repository. |
| `OLIVARES_VECTOR_API_KEY` | No | — | API key for the external vector index. |
| `OLIVARES_VECTOR_BACKEND` | No | — | Which vector index backs knowledge search. Unset keeps the in-process index, which is the air-gapped default. |
| `OLIVARES_VECTOR_DIM` | No | — | Vector dimension of the index, which has to match the embeddings model. |
| `OLIVARES_VECTOR_DSN` | No | — | Connection string for the external vector index. |
| `OLIVARES_VECTOR_NAMESPACE` | No | `knowledge_ann` | Table or collection the vector index writes to. |
| `OLIVARES_VECTOR_TIMEOUT` | No | — | Per-request timeout for the vector index, as a Go duration. |
| `OLIVARES_VOICE_CALL_CONFIG` | No | — | Path to the JSON configuration of the inbound voice webhook. |
| `OLIVARES_VOICE_DISPATCH_CONFIG` | No | — | Path to the JSON configuration of outbound voice dispatch. |
| `OLIVARES_WEBAUTHN_ORIGINS` | No | — | Comma-separated origins accepted for WebAuthn ceremonies. |
| `OLIVARES_WEBAUTHN_RPID` | No | — | WebAuthn relying-party id for the privileged-login flow. It has to match the site's registrable domain. |
| `OLIVARES_WEBAUTHN_RP_NAME` | No | — | Display name of the WebAuthn relying party, as shown by the authenticator. |
| `OLIVARES_WIF_BASE_URL` | No | — | Endpoint the workload identity exchange is performed against. |
| `OLIVARES_WIF_REFRESH_SLACK` | No | `60s` | How long before expiry a federated credential is refreshed, as a Go duration. |
| `OLIVARES_WIF_SPIFFE_SOCKET` | No | — | Path to the SPIFFE workload API socket the engine fetches its identity from. |
| `OLIVARES_WIF_TRUST_DOMAIN` | No | — | SPIFFE trust domain accepted for workload identity. |
| `OLIVARES_WORK_OUTBOX_INTERVAL` | No | — | How often the work-kernel outbox is drained, as a Go duration. `0` disables the pump. |
| `OLIVARES_WORK_RUN_REF` | No | — | Run reference the engine passes to a launched work session. Set by the engine per run, not by the operator. |
| `OLIVARES_WORK_SESSION_ID` | No | — | Session reference the engine passes to a launched work session. Set by the engine per run, not by the operator. |
| `OLIVARES_WORK_TOKEN` | No | — | Scoped token the engine passes to a launched work session. Set by the engine per run, not by the operator. |

### Variable families

These prefixes name families whose member variables are built at runtime — the per-provider and per-backend keys the engine composes from a provider name. The concrete members it composes are in the table above.

| Prefix | Required | Default | What it configures |
| --- | --- | --- | --- |
| `OLIVARES_AUDIT_ARCHIVE_` | No | — | Family prefix for the audit archive settings listed above. |
| `OLIVARES_CODEX_HOOK_` | No | — | Family prefix for the Codex hook client and server settings listed above. |
| `OLIVARES_DR_OFFSITE_` | No | — | Family prefix for the offsite replication settings listed above. |
| `OLIVARES_EMBEDDINGS` | No | — | Family stem for the unprefixed embeddings settings, which configure the OpenAI-compatible provider. |
| `OLIVARES_EMBEDDINGS_` | No | — | Family prefix from which the per-provider embeddings keys are built, by appending the provider name and then the setting. |
| `OLIVARES_GROK_HOOK_` | No | — | Family prefix for the Grok Build hook client and server settings listed above. |
| `OLIVARES_HOOK_PEP_` | No | — | Family prefix for the Claude Code hook client and server settings listed above. |
| `OLIVARES_KEY_WRAP` | No | — | Family stem naming the key management backend that wraps signing keys. |
| `OLIVARES_KEY_WRAP_` | No | — | Family prefix from which the per-backend key-wrapping keys are built. |
| `OLIVARES_LEDGER_KMS_` | No | — | Family prefix from which the per-backend ledger signer keys are built. |
| `OLIVARES_OIDC_` | No | — | Family prefix for the OIDC federation settings listed above. |
| `OLIVARES_OTEL_` | No | — | Family prefix for the trace export settings listed above. |
| `OLIVARES_SAML_` | No | — | Family prefix for the SAML federation settings listed above. |
| `OLIVARES_SESSION_RUNTIME_` | No | — | Family prefix for the session runtime settings listed above. |
| `OLIVARES_VECTOR_` | No | — | Family prefix for the vector index settings listed above. |
| `OLIVARES_WIF_` | No | — | Family prefix for the workload identity federation settings listed above. |
| `OLIVARES_WORK_` | No | — | Family prefix for the per-run values the engine passes into a launched work session. |

<!-- END GENERATED olivares-env-reference -->

## Store selection

The engine selects its store engine from `--engine`.

| Engine | When to use | Notes |
| --- | --- | --- |
| `sqlite` (default) | Single binary, single node, air-gapped installs. | Pure-Go embedded store; zero external dependencies. With no `--dsn`, the store file lives in the data directory. |
| `postgres` | Multi-tenant and scale-out deployments. | Adds row-level-security tenant isolation. Requires a least-privilege application role. |

SQLite is the default and needs no external service. Choosing `postgres` opts into the row-level-security backstop that isolates tenants: the engine **refuses to start** against a Postgres superuser or `BYPASSRLS` role unless that guard is explicitly overridden, because such a role would disable the tenant isolation backstop. The Compose Postgres overlay provisions the least-privilege application role on first init so this backstop is real.

:::tip[The default store is intentionally boring]
SQLite is not a toy default here. It is the air-gap-ready, zero-dependency store for the single-node topology, and it is the store the one-command Docker Compose deployment runs. Move to Postgres when you need multi-tenant isolation or horizontal scale, not before. See [self-hosting](/how-to/self-hosting/) and [the architecture overview](/explanation/architecture/overview/).
:::

## Audit checkpoint interval

The audit ledger is append-only, hash-chained, and anchored by Ed25519-signed checkpoints. `--checkpoint-interval` controls how often a signed checkpoint is written over every tenant chain (default `1h`; `0` disables checkpointing). A final shutdown checkpoint is written before the store closes, so the chain is anchored at clean shutdown as well as on the interval. The signed export and forwarding path is covered in [forward audit to Splunk](/how-to/forward-audit-to-splunk/).

## Secure defaults

These are the postures in effect with no configuration beyond `serve`. They are the product's default security stance, not optional hardening.

| Area | Default | What it means |
| --- | --- | --- |
| Credentials | None shipped | No default username or password exists. On first boot with no users, the engine mints a single-use setup token and prints it to standard output only — never to the logs. |
| First-boot setup | One-time token | The administrator creates the first user with that token, then logs in. The token is shown once and is single-use. |
| Transport | TLS on | HTTP and gRPC serve over TLS by default; a self-signed certificate is generated in the data directory if none is supplied, and both its certificate fingerprint and its `--pin-sha256` value are logged. |
| Bind address | Loopback | `--listen` and `--grpc-listen` default to `127.0.0.1`. The engine binds the local host until you deliberately publish it. |
| Plaintext mode | Off | `--insecure` is the only way to serve plaintext, and the gRPC path fails closed rather than degrade. Intended for localhost development only. |
| Demo seeding | Off | `--seed-demo` is off by default and refuses any non-loopback bind because it mints a public-password demo administrator. |
| Telemetry home | Off | The engine does not phone home: there is no telemetry-to-vendor channel, and nothing is sent as a side effect of running. Outbound connections exist to the sources you configure, plus `olivares upgrade` when you run it — it reaches the update channel unless `--endpoint` or `--bundle` points it elsewhere. That is what makes the [air-gapped install](/how-to/air-gap-install/) possible with zero egress. |

:::caution[Loopback by default, exposed by intent]
The default loopback binds mean the engine is not reachable off-host until you change them. When you do publish it — for example by mapping a host port in Docker Compose — that is a deliberate operator decision, and TLS is already on to protect it. Do not pair a published bind with `--insecure`.
:::

### First boot, in practice

On a fresh install the engine prints a `FIRST-BOOT SETUP` block to standard output containing the one-time setup token. The administrator uses it to create the first user, then authenticates. Under Docker Compose the token is read from the container logs:

```sh
docker compose -f deploy/compose/docker-compose.yml up -d
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
# then open https://localhost:8443 (self-signed TLS by default)
```

The setup endpoint and the login endpoint are part of the product's OpenAPI contract; see the [API reference](/reference/api/). The opaque session and API-key token model behind them is described in [the security model](/explanation/security/security-model/).

## What this page does not cover

This is the verified, common configuration surface. It does **not** enumerate every advanced flag for multi-node and mutual-TLS topologies — those belong to the distributed and air-gapped deployments described in [the architecture overview](/explanation/architecture/overview/) and listed in full in the [CLI reference](/reference/cli/). Where a setting is design-stage or topology-specific, it is documented there rather than presented here as a stable knob.

For the boundaries of what the product observes and where coverage is tiered, read [honesty and limits](/start/honesty-and-limits/).
