---
title: "Référence de configuration"
description: "La surface de configuration vérifiée du control plane Olivares AI : drapeaux de serve, variables d'environnement, sélection du stockage et valeurs par défaut sécurisées livrées d'origine."
---

Cette page documente la surface de configuration du moteur du control plane — le binaire Go unique nommé `olivares`. Elle couvre les drapeaux acceptés par la sous-commande `serve`, les variables d'environnement que le moteur lit au démarrage, la façon dont le stockage et le point de décision de politique (policy decision point) sont sélectionnés, et les valeurs par défaut sécurisées qui sont en vigueur sans aucune configuration.

Tout ce qui est listé ici est tiré des définitions de commandes propres au moteur et de sa racine de composition. Là où un paramètre ne peut pas être confirmé dans le source, il n'est pas listé. Pour la posture de sécurité conceptuelle derrière ces valeurs par défaut, voir [le modèle de sécurité](/fr/explanation/security/security-model/) ; pour le chemin exécutable de bout en bout, voir [l'auto-hébergement](/fr/how-to/self-hosting/).

:::note[Philosophie de configuration]
Le moteur est configuré par des drapeaux et par des variables d'environnement, pas
par un fichier de configuration tentaculaire. Toutes les variables qu'il lit sont
répertoriées ci-dessous, générées à partir des sources elles-mêmes. Les secrets qui
câblent les sources réelles restent dans des fichiers détenus par l'opérateur et
référencés par variable d'environnement — jamais dans le stockage. Les valeurs par
défaut sont choisies pour échouer en mode fermé (fail closed) : liaisons sur la boucle
locale, TLS activé, aucun identifiant par défaut.
:::

## La sous-commande `serve`

`olivares serve` exécute le serveur HTTP REST/web et le serveur gRPC dans un seul processus, avec l'interface web servie depuis la même origine que l'API. Les drapeaux ci-dessous sont les entrées de configuration vérifiées de cette commande.

| Drapeau | Défaut | Objet |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8443` | Adresse d'écoute HTTP (API REST + interface web embarquée). |
| `--grpc-listen` | `127.0.0.1:8444` | Adresse d'écoute gRPC (API du control plane / d'ingestion du collecteur). |
| `--data-dir` | `$OLIVARES_DATA_DIR`, une installation existante dans `./olivares-data`, sinon `$XDG_DATA_HOME/olivares` ou `~/.local/share/olivares` | Répertoire de données : clé de signature d'audit, matériel TLS, et (pour SQLite) le fichier de stockage. |
| `--engine` | `sqlite` | Moteur de stockage : `sqlite` ou `postgres`. |
| `--dsn` | vide (fichier SQLite dans le répertoire de données) | Chaîne de connexion au stockage. |
| `--checkpoint-interval` | `1h` | Fréquence d'écriture d'un point de contrôle d'audit signé sur la chaîne de chaque tenant. `0` désactive. |
| `--insecure` | off | Servir HTTP/gRPC en texte clair. Dangereux ; développement localhost uniquement. |
| `--seed-demo` | off | Charger un parc d'exemple synthétique pour les démos/E2E. Refuse de démarrer sur une liaison non-boucle-locale. |

TLS est activé par défaut. Sans `--tls-cert`/`--tls-key` fourni, le moteur s'assure une fois pour toutes, en amont, d'un certificat auto-signé dans le répertoire de données avant que tout écouteur n'accepte une connexion, de sorte que les serveurs HTTP et gRPC utilisent le même certificat et qu'aucun ne se rabat sur le texte clair. Lorsqu'il génère un certificat auto-signé, il journalise `cert_fingerprint_sha256` (le condensat du certificat, celui qu'affiche un navigateur) et `pin_sha256` (le condensat du SPKI du certificat feuille). `--pin-sha256` prend le second, en base64 ou en hexadécimal ; l'empreinte du certificat est un autre condensat — elle est analysée sans erreur, 32 octets dans les deux écritures, puis échoue au handshake avec `TLS SPKI pin mismatch`, qui indique la valeur à utiliser.

:::caution[`--insecure` est réservé à la boucle locale par conception]
`--insecure` sert HTTP et gRPC en texte clair, ce qui exposerait les jetons bearer sur le fil. Le chemin gRPC **échoue en mode fermé** : hors `--insecure`, le serveur refuse de construire un écouteur en texte clair plutôt que de se rétrograder silencieusement. N'utilisez `--insecure` que contre `127.0.0.1` pendant le développement local, jamais sur une adresse publiée.
:::

:::danger[`--seed-demo` est synthétique et auto-protecteur]
`--seed-demo` provisionne un administrateur de démo avec un **mot de passe public, présent dans l'arbre des sources** et des données de parc fabriquées. Il est réservé aux démos et au E2E. Le moteur refuse de le démarrer sur un écouteur non-boucle-locale : si `--listen` ou `--grpc-listen` n'est pas une adresse de boucle locale, la commande se termine sur une erreur. Utilisez un répertoire de données jetable ; ne le pointez jamais vers des données réelles.
:::

Une liste complète des drapeaux — y compris les drapeaux réservés à Postgres et au TLS mutuel utilisés dans les déploiements distribués — est dans la [référence CLI](/fr/reference/cli/). Cette page documente la surface de configuration commune ; certains drapeaux avancés régissent les topologies multi-nœuds décrites dans [l'aperçu de l'architecture](/fr/explanation/architecture/overview/).

## Variables d'environnement

Les trois groupes suivants sont ceux qu'un opérateur rencontre d'abord, décrits avec
leur comportement. L'inventaire complet leur fait suite, généré à partir des sources
propres au moteur afin qu'il ne puisse pas prendre de retard sur le binaire.

### Répertoire de données

| Variable | Effet |
| --- | --- |
| `OLIVARES_DATA_DIR` | Répertoire de données par défaut lorsque `--data-dir` n'est pas fourni. À défaut, le moteur utilise une installation existante dans `./olivares-data`, sinon `$XDG_DATA_HOME/olivares` ou `~/.local/share/olivares` — jamais le répertoire de travail courant, où il laisserait des clés privées. |

Le répertoire de données contient la clé de signature d'audit, le certificat et la clé TLS, et — pour le moteur SQLite — le fichier de stockage. Conservez-le entre les redémarrages.

### Câbler les sources réelles

| Variable | Effet |
| --- | --- |
| `OLIVARES_SOURCES_CONFIG` | Chemin vers un fichier JSON qui câble les sources d'observation réelles et les fournisseurs de roster d'identités avant le démarrage du moteur. |

`OLIVARES_SOURCES_CONFIG` est l'unique entrée par laquelle les sources de signaux non-démo et les fournisseurs de roster d'identités sont résolus. C'est la configuration porteuse de secrets de l'opérateur et elle est délibérément tenue hors du stockage. Le moteur la lit pendant le démarrage et enregistre chaque source **avant** le démarrage du runtime.

La gestion est honnête plutôt que de type fail-fast :

- Une variable **manquante** produit une configuration vide, et le moteur avertit que rien de réel n'est câblé.
- Un fichier **illisible ou au JSON invalide** avertit et produit une configuration vide — il n'interrompt jamais le démarrage.
- Une liste de sources configurée-mais-**vide** avertit qu'aucun connecteur n'ingérera, de sorte que le parc fonctionne sans trafic en direct, plutôt que de paraître silencieusement en bonne santé.
- Une liste d'**identités** vide avertit que le roster reste vide et que la synchronisation du roster est sans effet (no-op).

C'est voulu : une source non configurée fait remonter un avertissement au lieu de faire planter le control plane ou de feindre de fonctionner. Pour réellement peupler la carte d'accès (access map), configurez au moins une source — voir [connecter une source](/fr/how-to/connect-a-source/) et, pour le chemin coopératif de Claude Code via OpenTelemetry et MCP, [connecter Claude Code](/fr/how-to/connect-claude-code/).

### Point de décision d'autorisation (PDP)

Le point de décision de politique d'autorisation est sélectionné à la racine de composition par l'environnement. Le moteur natif de contrôle d'accès basé sur les attributs (ABAC) et le contrôle d'accès basé sur les rôles (RBAC) gouvernent toujours ; le PDP externe, lorsqu'il est sélectionné, est une couche supplémentaire **restreignant uniquement** (restrict-only) qui ne peut jamais élargir l'accès.

| Variable | Effet |
| --- | --- |
| `OLIVARES_PDP_ENGINE` | Sélectionne le PDP externe : `cedar`, `opa` ou `none` (vide ou `none` signifie ABAC natif uniquement). |
| `OLIVARES_PDP_CEDAR_FILE` | Moteur Cedar uniquement : chemin vers le fichier de politique Cedar de l'opérateur. |
| `OLIVARES_PDP_OPA_URL` | Moteur OPA uniquement : URL de base de l'endpoint Open Policy Agent. |
| `OLIVARES_PDP_OPA_PATH` | Moteur OPA uniquement : chemin de décision interrogé sous cet endpoint. |
| `OLIVARES_PDP_OPA_TOKEN` | Moteur OPA uniquement : jeton bearer pour l'endpoint OPA. |

Deux adaptateurs se trouvent derrière une même couture (seam) : un évaluateur **Cedar embarqué** (le chemin primaire, pur-Go) et un adaptateur **OPA-sur-HTTP**. L'opérateur choisit un moteur ; tous deux ne peuvent que restreindre, jamais élargir, la décision que le RBAC intégré a déjà prise.

:::note[Une mauvaise politique ne dé-gouverne jamais le plan]
Si `OLIVARES_PDP_ENGINE` sélectionne un moteur mais que sa configuration est invalide — un fichier Cedar illisible, une cible OPA malformée — le moteur **ne désactive que le PDP externe**, maintient le moteur ABAC natif et le RBAC en application, et journalise bruyamment. Un fichier de politique cassé ne laisse jamais silencieusement des requêtes non gouvernées et ne fait jamais planter le control plane.
:::

Pour le modèle deny-by-default (refus par défaut), la nature privilégiée de la consultation du graphe d'accès, et la façon dont chaque lecture d'autorisation est auditée, voir [le modèle de sécurité](/fr/explanation/security/security-model/).

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

## Sélection du stockage

Le moteur sélectionne son moteur de stockage à partir de `--engine`.

| Moteur | Quand l'utiliser | Notes |
| --- | --- | --- |
| `sqlite` (défaut) | Binaire unique, nœud unique, installations en environnement isolé. | Stockage embarqué pur-Go ; zéro dépendance externe. Sans `--dsn`, le fichier de stockage vit dans le répertoire de données. |
| `postgres` | Déploiements multi-tenant et à montée en charge. | Ajoute l'isolation de tenant par sécurité au niveau des lignes (row-level security). Requiert un rôle applicatif à moindre privilège. |

SQLite est le défaut et ne nécessite aucun service externe. Choisir `postgres` souscrit au filet de sécurité par sécurité au niveau des lignes qui isole les tenants : le moteur **refuse de démarrer** contre un superutilisateur Postgres ou un rôle `BYPASSRLS` sauf si ce garde-fou est explicitement contourné, car un tel rôle désactiverait le filet de sécurité d'isolation des tenants. L'overlay Postgres de Compose provisionne le rôle applicatif à moindre privilège au premier init pour que ce filet de sécurité soit réel.

:::tip[Le stockage par défaut est volontairement ennuyeux]
SQLite n'est pas un défaut jouet ici. C'est le stockage prêt pour l'environnement isolé, sans dépendance, pour la topologie mono-nœud, et c'est le stockage que fait tourner le déploiement Docker Compose en une commande. Passez à Postgres quand vous avez besoin d'isolation multi-tenant ou de mise à l'échelle horizontale, pas avant. Voir [l'auto-hébergement](/fr/how-to/self-hosting/) et [l'aperçu de l'architecture](/fr/explanation/architecture/overview/).
:::

## Intervalle des points de contrôle d'audit

Le registre d'audit est en ajout seul (append-only), chaîné par hachage (hash-chained), et ancré par des points de contrôle signés en Ed25519. `--checkpoint-interval` contrôle la fréquence d'écriture d'un point de contrôle signé sur la chaîne de chaque tenant (défaut `1h` ; `0` désactive les points de contrôle). Un point de contrôle final d'arrêt est écrit avant la fermeture du stockage, de sorte que la chaîne est ancrée à l'arrêt propre aussi bien que sur l'intervalle. Le chemin d'export signé et de transfert est traité dans [transférer l'audit vers Splunk](/fr/how-to/forward-audit-to-splunk/).

## Valeurs par défaut sécurisées

Ce sont les postures en vigueur sans aucune configuration au-delà de `serve`. Ce sont la position de sécurité par défaut du produit, pas un durcissement optionnel.

| Domaine | Défaut | Ce que cela signifie |
| --- | --- | --- |
| Identifiants | Aucun livré | Aucun nom d'utilisateur ni mot de passe par défaut n'existe. Au premier démarrage sans utilisateurs, le moteur émet un jeton de configuration à usage unique et l'affiche sur la sortie standard uniquement — jamais dans les journaux. |
| Configuration au premier démarrage | Jeton à usage unique | L'administrateur crée le premier utilisateur avec ce jeton, puis se connecte. Le jeton est affiché une fois et est à usage unique. |
| Transport | TLS activé | HTTP et gRPC servent sur TLS par défaut ; un certificat auto-signé est généré dans le répertoire de données si aucun n'est fourni, et son empreinte de certificat ainsi que sa valeur `--pin-sha256` sont journalisées. |
| Adresse de liaison | Boucle locale | `--listen` et `--grpc-listen` se lient par défaut à `127.0.0.1`. Le moteur se lie à l'hôte local jusqu'à ce que vous le publiiez délibérément. |
| Mode texte clair | Off | `--insecure` est le seul moyen de servir en texte clair, et le chemin gRPC échoue en mode fermé plutôt que de se rétrograder. Destiné au développement localhost uniquement. |
| Amorçage de démo | Off | `--seed-demo` est désactivé par défaut et refuse toute liaison non-boucle-locale parce qu'il émet un administrateur de démo à mot de passe public. |
| Télémétrie « phone home » | Off | Le moteur ne « rappelle pas à la maison » : il n'existe aucun canal de télémétrie vers l'éditeur, et rien n'est envoyé comme effet de bord de l'exécution. Les connexions sortantes existent vers les sources que vous configurez, plus `olivares upgrade` lorsque vous le lancez — il joint le canal de mises à jour sauf si `--endpoint` ou `--bundle` le pointe ailleurs. C'est ce qui rend possible l'[installation en environnement isolé](/fr/how-to/air-gap-install/) avec zéro sortie (egress). |

:::caution[Boucle locale par défaut, exposé à dessein]
Les liaisons par défaut sur la boucle locale signifient que le moteur n'est pas joignable hors de l'hôte tant que vous ne les changez pas. Quand vous le publiez — par exemple en mappant un port hôte dans Docker Compose — c'est une décision délibérée de l'opérateur, et TLS est déjà activé pour le protéger. Ne couplez pas une liaison publiée avec `--insecure`.
:::

### Premier démarrage, en pratique

Sur une installation neuve, le moteur affiche un bloc `FIRST-BOOT SETUP` sur la sortie standard contenant le jeton de configuration à usage unique. L'administrateur l'utilise pour créer le premier utilisateur, puis s'authentifie. Sous Docker Compose, le jeton est lu depuis les journaux du conteneur :

```sh
docker compose -f deploy/compose/docker-compose.yml up -d
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
# puis ouvrez https://localhost:8443 (TLS auto-signé par défaut)
```

L'endpoint de configuration et l'endpoint de connexion font partie du contrat OpenAPI du produit ; voir la [Référence de l'API](/reference/api/). Le modèle de jetons opaques de session et de clé d'API qui les sous-tend est décrit dans [le modèle de sécurité](/fr/explanation/security/security-model/).

## Ce que cette page ne couvre pas

C'est la surface de configuration commune et vérifiée. Elle **n'énumère pas** chaque drapeau avancé pour les topologies multi-nœuds et à TLS mutuel — ceux-ci appartiennent aux déploiements distribués et en environnement isolé décrits dans [l'aperçu de l'architecture](/fr/explanation/architecture/overview/) et listés en intégralité dans la [référence CLI](/fr/reference/cli/). Là où un paramètre est en phase de conception ou spécifique à une topologie, il y est documenté plutôt que présenté ici comme un réglage stable.

Pour les limites de ce que le produit observe et l'endroit où la couverture est échelonnée, lisez [honnêteté et limites](/fr/start/honesty-and-limits/).
