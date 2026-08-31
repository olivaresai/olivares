---
title: "Konfigurationsreferenz"
description: "Die verifizierte Konfigurationsschnittstelle der Olivares-AI-Control-Plane: serve-Flags, Umgebungsvariablen, Store-Auswahl und die sicheren Defaults, die out of the box ausgeliefert werden."
---

Diese Seite dokumentiert die Konfigurationsschnittstelle der Control-Plane-Engine — des einzelnen Go-Binarys namens `olivares`. Sie behandelt die vom `serve`-Subcommand akzeptierten Flags, die Umgebungsvariablen, die die Engine beim Boot liest, wie der Store und der Policy Decision Point ausgewählt werden, und die sicheren Defaults, die ganz ohne Konfiguration in Kraft sind.

Alles hier Aufgeführte stammt aus den eigenen Befehlsdefinitionen und der Composition Root der Engine. Wo eine Einstellung im Quellcode nicht bestätigt werden kann, wird sie nicht aufgeführt. Für die konzeptionelle Sicherheitshaltung hinter diesen Defaults siehe [das Security-Modell](/de/explanation/security/security-model/); für den ausführbaren End-to-End-Pfad siehe [Self-Hosting](/de/how-to/self-hosting/).

:::note[Konfigurationsphilosophie]
Die Engine wird über Flags und Umgebungsvariablen konfiguriert, nicht über eine wuchernde Konfigurationsdatei. Jede Variable, die sie liest, ist unten aufgeführt und aus den Quellen selbst generiert. Secrets, die echte Sources verdrahten, bleiben in betreibergehaltenen Dateien, auf die per Umgebungsvariable verwiesen wird — nie im Store. Die Defaults sind so gewählt, dass sie fehl-geschlossen (fail closed) sind: Loopback-Binds, TLS an, keine Default-Credentials.
:::

## Der `serve`-Subcommand

`olivares serve` betreibt den REST/Web-HTTP-Server und den gRPC-Server in einem Prozess, wobei die Web-UI vom selben Origin wie die API ausgeliefert wird. Die Flags unten sind die verifizierten Konfigurationseingaben für diesen Befehl.

| Flag | Default | Zweck |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8443` | HTTP-Listen-Adresse (REST API + eingebettete Web-UI). |
| `--grpc-listen` | `127.0.0.1:8444` | gRPC-Listen-Adresse (Control-Plane- / Collector-Ingest-API). |
| `--data-dir` | `$OLIVARES_DATA_DIR`, eine vorhandene Installation in `./olivares-data`, sonst `$XDG_DATA_HOME/olivares` oder `~/.local/share/olivares` | Datenverzeichnis: Audit-Signaturschlüssel, TLS-Material und (bei SQLite) die Store-Datei. |
| `--engine` | `sqlite` | Store-Engine: `sqlite` oder `postgres`. |
| `--dsn` | leer (SQLite-Datei im Datenverzeichnis) | Store-Verbindungsstring. |
| `--checkpoint-interval` | `1h` | Wie oft ein signierter Audit-Checkpoint über jede Tenant-Chain geschrieben wird. `0` deaktiviert. |
| `--insecure` | aus | Stellt Klartext-HTTP/gRPC bereit. Gefährlich; nur für localhost-Entwicklung. |
| `--seed-demo` | aus | Lädt eine synthetische Beispiel-Estate für Demos/E2E. Weigert sich, auf einem Nicht-Loopback-Bind zu starten. |

TLS ist standardmäßig an. Ohne bereitgestelltes `--tls-cert`/`--tls-key` stellt die Engine einmal, im Voraus, ein selbstsigniertes Zertifikat im Datenverzeichnis sicher, bevor irgendein Listener eine Verbindung akzeptiert, sodass sowohl der HTTP- als auch der gRPC-Server dasselbe Zertifikat verwenden und keiner auf Klartext zurückfällt. Wenn sie ein selbstsigniertes Zertifikat generiert, protokolliert sie `cert_fingerprint_sha256` (den Zertifikats-Hash, den ein Browser anzeigt) und `pin_sha256` (den SPKI-Hash des Leaf-Zertifikats). `--pin-sha256` nimmt den zweiten, in Base64 oder Hex; der Zertifikatsfingerprint ist ein anderer Hash — er wird geparst, 32 Byte in beiden Schreibweisen, und scheitert dann beim Handshake mit `TLS SPKI pin mismatch`, was den zu verwendenden Wert nennt.

:::caution[`--insecure` ist absichtlich nur für Loopback]
`--insecure` stellt Klartext-HTTP und -gRPC bereit, was Bearer-Token auf der Leitung offenlegen würde. Der gRPC-Pfad **schlägt fehl-geschlossen (fail closed) fehl**: außerhalb von `--insecure` weigert sich der Server, einen Klartext-Listener zu konstruieren, statt stillschweigend herabzustufen. Verwenden Sie `--insecure` nur gegen `127.0.0.1` während der lokalen Entwicklung, nie auf einer veröffentlichten Adresse.
:::

:::danger[`--seed-demo` ist synthetisch und selbstschützend]
`--seed-demo` provisioniert einen Demo-Administrator mit einem **öffentlichen Passwort aus dem Quellbaum** und fabrizierten Estate-Daten. Es ist nur für Demos und E2E gedacht. Die Engine weigert sich, es auf einem Nicht-Loopback-Listener zu starten: wenn entweder `--listen` oder `--grpc-listen` keine Loopback-Adresse ist, beendet sich der Befehl mit einem Fehler. Verwenden Sie ein Wegwerf-Datenverzeichnis; richten Sie es nie auf echte Daten.
:::

Eine vollständige Flag-Auflistung — einschließlich der nur-Postgres- und mutual-TLS-Flags, die in verteilten Deployments verwendet werden — steht in der [CLI-Referenz](/de/reference/cli/). Diese Seite dokumentiert die übliche Konfigurationsschnittstelle; einige fortgeschrittene Flags steuern Multi-Node-Topologien, die in [der Architektur-Übersicht](/de/explanation/architecture/overview/) beschrieben sind.

## Umgebungsvariablen

Die drei folgenden Gruppen sind die Variablen, denen ein Operator zuerst begegnet; ihr Verhalten ist ausgeschrieben. Danach folgt das vollständige, aus den eigenen Quellen der Engine generierte Verzeichnis, damit es nicht hinter dem Binary zurückbleiben kann.

### Datenverzeichnis

| Variable | Wirkung |
| --- | --- |
| `OLIVARES_DATA_DIR` | Default-Datenverzeichnis, wenn `--data-dir` nicht angegeben ist. Ohne beides nutzt die Engine eine vorhandene `./olivares-data`-Installation, sonst `$XDG_DATA_HOME/olivares` bzw. `~/.local/share/olivares` — niemals das aktuelle Arbeitsverzeichnis, wo sie private Schlüssel hinterlassen würde. |

Das Datenverzeichnis hält den Audit-Signaturschlüssel, das TLS-Zertifikat und den Schlüssel und — bei der SQLite-Engine — die Store-Datei. Erhalten Sie es über Neustarts hinweg.

### Echte Sources verdrahten

| Variable | Wirkung |
| --- | --- |
| `OLIVARES_SOURCES_CONFIG` | Pfad zu einer JSON-Datei, die echte Beobachtungs-Sources und Identity-Roster-Provider verdrahtet, bevor die Engine startet. |

`OLIVARES_SOURCES_CONFIG` ist die einzige Eingabe, über die Nicht-Demo-Signal-Sources und Identity-Roster-Provider aufgelöst werden. Es ist die secret-tragende Konfiguration des Betreibers und wird bewusst aus dem Store herausgehalten. Die Engine liest sie während des Boots und registriert jede Source, **bevor** die Runtime startet.

Die Behandlung ist ehrlich statt fail-fast:

- Eine **fehlende** Variable ergibt eine leere Konfiguration, und die Engine warnt, dass nichts Echtes verdrahtet ist.
- Eine **unlesbare oder ungültige-JSON**-Datei warnt und ergibt eine leere Konfiguration — sie bricht den Boot nie ab.
- Eine konfigurierte-aber-**leere** Source-Liste warnt, dass kein Connector ingestieren wird, sodass die Estate auf keinem Live-Traffic läuft, statt stillschweigend gesund zu wirken.
- Eine leere **Identity**-Liste warnt, dass der Roster leer bleibt und die Roster-Synchronisation ein No-op ist.

Das ist Absicht: eine unkonfigurierte Source bringt eine Warnung an die Oberfläche, statt die Control Plane abstürzen zu lassen oder vorzugeben zu funktionieren. Um die Zugriffskarte (Access Map) tatsächlich zu befüllen, konfigurieren Sie mindestens eine Source — siehe [Eine Quelle anbinden](/de/how-to/connect-a-source/) und, für den kooperativen Claude-Code-Pfad über OpenTelemetry und MCP, [Claude Code anbinden](/de/how-to/connect-claude-code/).

### Autorisierungs-Entscheidungspunkt (PDP)

Der Policy Decision Point für Autorisierung wird an der Composition Root per Umgebung ausgewählt. Die native attributbasierte Zugriffskontrolle (ABAC) und die rollenbasierte Zugriffskontrolle (RBAC) regieren immer; der externe PDP ist, wenn ausgewählt, eine zusätzliche **restrict-only**-Schicht, die den Zugriff nie erweitern kann.

| Variable | Wirkung |
| --- | --- |
| `OLIVARES_PDP_ENGINE` | Wählt den externen PDP aus: `cedar`, `opa` oder `none` (leer oder `none` bedeutet nur natives ABAC). |
| `OLIVARES_PDP_CEDAR_FILE` | Nur Cedar-Engine: Pfad zur Cedar-Policy-Datei des Betreibers. |
| `OLIVARES_PDP_OPA_URL` | Nur OPA-Engine: Basis-URL des Open-Policy-Agent-Endpunkts. |
| `OLIVARES_PDP_OPA_PATH` | Nur OPA-Engine: Entscheidungspfad, der unter diesem Endpunkt abgefragt wird. |
| `OLIVARES_PDP_OPA_TOKEN` | Nur OPA-Engine: Bearer-Token für den OPA-Endpunkt. |

Zwei Adapter sitzen hinter einer Naht: ein **eingebetteter Cedar**-Evaluator (der primäre, pure-Go-Pfad) und ein **OPA-over-HTTP**-Adapter. Der Betreiber wählt eine Engine; beide können die Entscheidung, die das eingebaute RBAC bereits getroffen hat, nur einschränken, nie erweitern.

:::note[Eine schlechte Policy ent-governt die Plane nie]
Wenn `OLIVARES_PDP_ENGINE` eine Engine auswählt, deren Konfiguration aber ungültig ist — eine unlesbare Cedar-Datei, ein fehlerhaftes OPA-Target —, **deaktiviert die Engine nur den externen PDP**, hält die native ABAC-Engine und RBAC durchsetzend und protokolliert lautstark. Eine kaputte Policy-Datei lässt Requests nie stillschweigend ungoverned und bringt die Control Plane nie zum Absturz.
:::

Für das Deny-by-Default-Modell, die privilegierte Natur des Betrachtens des Zugriffsgraphen (Access Graph) und wie jeder Autorisierungs-Read auditiert wird, siehe [das Security-Modell](/de/explanation/security/security-model/).

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

## Store-Auswahl

Die Engine wählt ihre Store-Engine aus `--engine`.

| Engine | Wann verwenden | Hinweise |
| --- | --- | --- |
| `sqlite` (Default) | Einzelnes Binary, Single Node, air-gapped Installationen. | Pure-Go-eingebetteter Store; null externe Abhängigkeiten. Ohne `--dsn` liegt die Store-Datei im Datenverzeichnis. |
| `postgres` | Multi-Tenant- und Scale-out-Deployments. | Fügt Row-Level-Security-Tenant-Isolation hinzu. Erfordert eine Least-Privilege-Anwendungsrolle. |

SQLite ist der Default und braucht keinen externen Dienst. `postgres` zu wählen entscheidet sich für die Row-Level-Security-Absicherung, die Tenants isoliert: die Engine **weigert sich zu starten** gegen einen Postgres-Superuser oder eine `BYPASSRLS`-Rolle, sofern dieser Schutz nicht ausdrücklich überschrieben wird, da eine solche Rolle die Tenant-Isolations-Absicherung deaktivieren würde. Das Compose-Postgres-Overlay provisioniert beim ersten Init die Least-Privilege-Anwendungsrolle, damit diese Absicherung real ist.

:::tip[Der Default-Store ist absichtlich langweilig]
SQLite ist hier kein Spielzeug-Default. Es ist der air-gap-fähige, abhängigkeitsfreie Store für die Single-Node-Topologie, und es ist der Store, den das Ein-Befehl-Docker-Compose-Deployment ausführt. Wechseln Sie zu Postgres, wenn Sie Multi-Tenant-Isolation oder horizontale Skalierung brauchen, nicht früher. Siehe [Self-Hosting](/de/how-to/self-hosting/) und [die Architektur-Übersicht](/de/explanation/architecture/overview/).
:::

## Audit-Checkpoint-Intervall

Der Audit-Ledger ist append-only, hash-chained und durch Ed25519-signierte Checkpoints verankert. `--checkpoint-interval` steuert, wie oft ein signierter Checkpoint über jede Tenant-Chain geschrieben wird (Default `1h`; `0` deaktiviert das Checkpointing). Ein finaler Shutdown-Checkpoint wird geschrieben, bevor der Store schließt, sodass die Chain sowohl beim sauberen Shutdown als auch im Intervall verankert ist. Der signierte Export- und Weiterleitungspfad wird in [Audit an Splunk weiterleiten](/de/how-to/forward-audit-to-splunk/) behandelt.

## Sichere Defaults

Dies sind die Haltungen, die ganz ohne Konfiguration über `serve` hinaus in Kraft sind. Sie sind die Default-Sicherheitsposition des Produkts, kein optionales Hardening.

| Bereich | Default | Was es bedeutet |
| --- | --- | --- |
| Credentials | Keine ausgeliefert | Es existiert kein Default-Benutzername und kein Default-Passwort. Beim ersten Boot ohne Nutzer prägt die Engine ein single-use Setup-Token und gibt es nur auf Standard-Output aus — nie in die Logs. |
| First-Boot-Setup | Einmaliges Token | Der Administrator erstellt mit diesem Token den ersten Nutzer und meldet sich dann an. Das Token wird einmal angezeigt und ist single-use. |
| Transport | TLS an | HTTP und gRPC liefern standardmäßig über TLS aus; ein selbstsigniertes Zertifikat wird im Datenverzeichnis generiert, wenn keines bereitgestellt wird, und sowohl sein Zertifikatsfingerprint als auch sein `--pin-sha256`-Wert werden protokolliert. |
| Bind-Adresse | Loopback | `--listen` und `--grpc-listen` binden standardmäßig auf `127.0.0.1`. Die Engine bindet den lokalen Host, bis Sie sie bewusst veröffentlichen. |
| Klartext-Modus | Aus | `--insecure` ist die einzige Möglichkeit, Klartext auszuliefern, und der gRPC-Pfad schlägt fehl-geschlossen (fail closed) fehl, statt herabzustufen. Nur für localhost-Entwicklung gedacht. |
| Demo-Seeding | Aus | `--seed-demo` ist standardmäßig aus und verweigert jeden Nicht-Loopback-Bind, weil es einen Demo-Administrator mit öffentlichem Passwort prägt. |
| Telemetrie nach Hause | Aus | Die Engine telefoniert nicht nach Hause: Es gibt keinen Telemetrie-zum-Hersteller-Kanal, und im laufenden Betrieb wird nichts als Nebeneffekt gesendet. Ausgehende Verbindungen bestehen zu den Sources, die Sie konfigurieren, sowie zu `olivares upgrade`, wenn Sie es ausführen — es ruft den Update-Kanal auf, sofern `--endpoint` oder `--bundle` nicht woanders hinzeigt. Das ist es, was die [air-gapped Installation](/de/how-to/air-gap-install/) mit null Egress möglich macht. |

:::caution[Standardmäßig Loopback, absichtlich exponiert]
Die Default-Loopback-Binds bedeuten, dass die Engine off-host nicht erreichbar ist, bis Sie sie ändern. Wenn Sie sie veröffentlichen — zum Beispiel durch das Mappen eines Host-Ports in Docker Compose —, ist das eine bewusste Betreiberentscheidung, und TLS ist bereits an, um sie zu schützen. Paaren Sie einen veröffentlichten Bind nicht mit `--insecure`.
:::

### First Boot, in der Praxis

Bei einer frischen Installation gibt die Engine einen `FIRST-BOOT SETUP`-Block auf Standard-Output aus, der das einmalige Setup-Token enthält. Der Administrator verwendet es, um den ersten Nutzer zu erstellen, und authentifiziert sich dann. Unter Docker Compose wird das Token aus den Container-Logs gelesen:

```sh
docker compose -f deploy/compose/docker-compose.yml up -d
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
# dann https://localhost:8443 öffnen (standardmäßig selbstsigniertes TLS)
```

Der Setup-Endpunkt und der Login-Endpunkt sind Teil des OpenAPI-Vertrags des Produkts; siehe die [API-Referenz](/reference/api/). Das opake Session- und API-Key-Token-Modell dahinter wird in [dem Security-Modell](/de/explanation/security/security-model/) beschrieben.

## Was diese Seite nicht abdeckt

Dies ist die verifizierte, übliche Konfigurationsschnittstelle. Sie zählt **nicht** jedes fortgeschrittene Flag für Multi-Node- und mutual-TLS-Topologien auf — die gehören zu den verteilten und air-gapped Deployments, die in [der Architektur-Übersicht](/de/explanation/architecture/overview/) beschrieben und vollständig in der [CLI-Referenz](/de/reference/cli/) aufgelistet sind. Wo eine Einstellung im Design-Stadium oder topologiespezifisch ist, wird sie dort dokumentiert, statt hier als stabiler Knopf präsentiert zu werden.

Für die Grenzen dessen, was das Produkt beobachtet, und wo die Abdeckung gestuft ist, lesen Sie [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/).
