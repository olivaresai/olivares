---
title: Catalogue des connecteurs et niveaux de couverture
description: >-
  Les connecteurs first-party que le control plane peut câbler aujourd'hui,
  regroupés par le niveau de couverture honnête que chacun prend en charge —
  clean, lossy, impossible-passively, cooperative et approximate-by-attribution —
  ainsi que les destinations de sortie.
---

Cette page est le **catalogue** des connecteurs first-party et, pour chacun, le
**niveau de couverture honnête** qu'il peut prendre en charge. Elle complète
[connecter une source](/fr/how-to/connect-a-source/), qui explique le *modèle* de
connecteur (observe-only, minimal-data, les trois types d'observation) — lisez-la
d'abord. Cette page répond à la question suivante : *quelles sources existent, et à
quel point le signal de chacune est-il bon ?*

La couverture est **classée par niveaux selon ce que la surface d'audit d'un système
peut honnêtement vous dire**, jamais selon ce qu'on voudrait qu'elle puisse dire. Les
niveaux, tels qu'employés dans toute la documentation :

- **Cooperative** — un agent ou une plateforme qui rapporte ce qu'il a fait
  (OpenTelemetry, une API admin d'un éditeur). Fidélité la plus élevée *lorsqu'elle est
  présente* ; dépend de la coopération de la source.
- **Clean** — un store qui classe lecture vs écriture **nativement**, repris tel quel
  depuis sa propre piste d'audit (audit SQL, journaux d'accès aux données d'un object-store
  / entrepôt).
- **Lossy** — un store dont l'audit ne sépare pas proprement lecture et écriture, ni
  appelant et appelant (document stores, lineage). Les arêtes apparaissent, mais souvent
  en `approximate`.
- **Impossible passively** — un système sans surface d'audit passive exploitable (caches
  en mémoire, bases de données embarquées en fichier unique). Il n'y a pas de signal
  read-first honnête ; le produit ne prétend pas le contraire.
- **Approximate-by-attribution** — l'accès est réel mais l'attribution se fait à un rôle,
  un processus ou une credential partagée, et non à un agent résolu ; l'arête est donc
  `approximate`.
- **Untrusted hint** — une capacité déclarée (une annotation d'outil MCP), corroborée,
  jamais accordée seule.

:::caution[Ce que reflète ce catalogue : les connecteurs câblés dans le build actuel]
Cette liste recense les connecteurs **enregistrés dans le jeu de connecteurs du binaire
par défaut** aujourd'hui — c'est-à-dire les kinds que vous pouvez nommer dans
`OLIVARES_SOURCES_CONFIG` et faire câbler par le moteur. Le produit est en pré-1.0. Les
connecteurs canoniques de l'access-map R/RW — **pgAudit**, **S3/CloudTrail**, le
backstop **eBPF/Tetragon**, l'inventaire **runtime** et l'introspection **MCP** — ainsi
que les **sources de documents de connaissance** sont désormais câblés et configurables
dans un `serve` standard ; certains comportent des **exigences de déploiement** (un
capteur Tetragon, un accès hôte) couvertes dans
[Exigences de déploiement](#exigences-de-déploiement-et-attribution-honnête) ci-dessous.
La couverture est **classée honnêtement par niveaux** : la présence d'un connecteur ici
n'est pas une affirmation d'attribution per-agent ferme, qui reste la dépendance dure (un
compte partagé fait retomber même un store de niveau clean à `approximate`).
:::

## Cooperative — télémétrie Claude et éditeurs

Les sources de plus haute fidélité lorsqu'elles sont présentes. La source runtime de
Claude Code s'exécute **hors processus** sous forme de plugin embarqué (un simple build de
dev l'omet et le boot le signale honnêtement plutôt que d'avoir l'air sain).

| Kind | Observe | Notes |
|---|---|---|
| `claude` | Télémétrie d'outils OTLP de Claude Code + introspection MCP → arêtes / coût / findings | Plugin hors processus ; `attributed` quand une identité per-agent est présente, sinon `approximate` |
| `claude-api` | Échantillons de coût de l'Admin API Claude + findings de posture de gouvernance | In-process ; no-op hors ligne (pas de clé admin) |
| `claude-compliance` | Preuves du flux d'activité Compliance de Claude → findings | GET-only par construction ; no-op hors ligne |
| `claude-config` | Arbre de configuration Claude statique (subagents / Skills / plugins) → arêtes de **capacité déclarée** | Métadonnées uniquement — une surface de capacité, pas un accès observé |
| `claude-console` | IAM de l'organisation Claude → findings de posture SSO/SCIM (roster d'identités + source) | |
| `claude-wif` | Roster d'identités non-humaines / workload-identity Anthropic + arêtes de scope permis | Modélise la fédération déclarée par l'opérateur ; signale les pièges des clés statiques |
| `claude-managed-agents` | Inventaire des managed agents Claude + événements de threads (récepteur webhook + pollers GET) | Source en streaming (`poll_seconds: 0`) ; hors ligne, c'est un no-op |
| `claude-projects` | Inventaire des Projects de Claude Organization (appartenance / clés API) + politique de projet déclarée par l'opérateur | Admin API en lecture seule ; no-op hors ligne |
| `claude-apps-gateway` | Posture d'apps-gateway Claude, octrois de modèles déclarés et ingest d'événements d'audit → topologie + findings | Lit un `gateway.yaml` existant et un export d'audit JSONL facultatif |
| `claude-batch` | Inventaire d'Anthropic Message Batches + Files API, application des politiques de batch et expiration de la rétention des uploads | Ne lit jamais les payloads ni le contenu des fichiers ; finding hors ligne honnête sans clé admin |
| `claude-routines` | Inventaire de Claude Code Routines (déclencheurs planifiés) → arêtes + findings de cadence/revue | GET-only ; le contenu des prompts est uniquement haché ; streaming (`poll_seconds: 0`) |
| `cowork` | Récepteur de logs OTLP/HTTP Claude Cowork → preuves d'activité | Plugin hors processus (isolation des dépendances OTel-proto) |
| `cowork-analytics` | Analytique d'engagement Claude Cowork | In-process (client modelprovider uniquement) |
| `codex` | Échantillons de coût OpenAI Codex, preuves d'usage/auth/audit admin et findings d'adoption | Admin API en lecture seule ; les surfaces soumises à la vente se dégradent en finding de posture |
| `cursor` | Coût facturé par l'Admin API Cursor, journaux d'audit de l'équipe, inventaire des membres et posture budgétaire | Un 403/404 lié au plan se dégrade en finding, sans jamais faire échouer la source |

### Profil framework GenAI vendor-neutral (`gen_ai.*`) — opt-in

Les frameworks d'agents que le catalogue promet — **LangGraph / LangChain, CrewAI,
AutoGen / Microsoft Agent Framework, Google ADK** (et le SDK OpenAI, LlamaIndex,
Pydantic-AI, Strands, …) — n'émettent **pas** le schéma `claude_code.*` de Claude. Ils
convergent vers les [conventions sémantiques OpenTelemetry **GenAI**](https://github.com/open-telemetry/semantic-conventions-genai)
(`gen_ai.*`). La même source `claude` ingère aussi ce profil, si bien qu'une flotte
instrumentée OTel alimente l'**access map** et les **FinOps** via un seul ingest plutôt
qu'un connecteur sur mesure par framework — l'intégration au plus fort effet de levier.

**Ce profil est OPT-IN et honnêtement étiqueté expérimental.** Toute la zone `gen_ai`
est au statut OpenTelemetry **Development** (pas Stable, juin 2026), elle ne s'active donc
que lorsque vous reproduisez le propre gate de la spec. Réglez le `semconv_opt_in` du
connecteur sur une liste séparée par des virgules contenant le token
`gen_ai_latest_experimental` (en miroir de `OTEL_SEMCONV_STABILITY_OPT_IN`). Désactivé par
défaut, un signal `gen_ai.*` alimente quand même le watchdog de silence mais ne mappe
aucune arête/coût — nous ne revendiquons jamais une stabilité que les conventions n'ont
pas.

Parce que les conventions sont en pleine mutation, l'ingest est **dual-name** (il lit la
clé actuelle *et* le prédécesseur déprécié encore émis dans la nature) et **multi-signal**
(il mappe les **spans** de trace, l'**event** de log `gen_ai.client.inference.operation.details`
et reconnaît les **métriques** client) :

| Ce qu'il lit | Clé actuelle | Aussi accepté (déprécié, encore émis par) |
|---|---|---|
| Provider | `gen_ai.provider.name` | `gen_ai.system` (défaut v1.36.0-ou-antérieur ; **Google ADK**, p. ex. `gcp.gemini`) |
| Tokens d'entrée | `gen_ai.usage.input_tokens` | `gen_ai.usage.prompt_tokens` (**OpenLLMetry/Traceloop** → LangChain/LangGraph/CrewAI) |
| Tokens de sortie | `gen_ai.usage.output_tokens` | `gen_ai.usage.completion_tokens` (idem) |

| Attribut gen_ai | mappe vers | confiance |
|---|---|---|
| `gen_ai.usage.*` (tokens) | `CostSample` (provenance **estimated** — tokens, pas coût facturé) | — |
| `gen_ai.provider.name` / `request.model` / `response.model` | provider de coût + modèle (response préféré) | — |
| `gen_ai.operation.name = execute_tool` + `gen_ai.tool.name` | **arête d'accès** agent→outil (mode `unknown`) | `attributed` |
| `gen_ai.conversation.id` + `gen_ai.agent.{name,id}` | **arête d'attribution** conversation→agent + ref de session | `attributed` |

#### Matrice de dialectes supportés (normalizer multi-génération)

Les conventions GenAI ont changé en **trois générations qui coexistent** dans les flottes
réelles de 2026. L'ingest détecte la génération **par signal** à partir de marqueurs
exclusifs à chaque génération et estampille l'event normalisé avec le pin semconv
correspondant (le finding de posture `genai.semconv` enregistre le jeu actif par run ; un
finding `drift` info par run signale chaque dialecte **déprécié** vu, afin que vous sachiez
quelles flottes doivent mettre à niveau leur instrumentation). Le **contenu** des messages
**n'est jamais lu, quelle que soit la génération** — les clés de contenu ne servent que de
marqueurs de dialecte (posture minimal-data).

| Dialecte détecté | Pin estampillé | Marqueurs exclusifs (vérifiés) | Émis par (vérifié juin 2026) |
|---|---|---|---|
| Legacy **OpenLLMetry/Traceloop** (pre-semconv) | `openllmetry` | `gen_ai.prompt.{i}.*` / `gen_ai.completion.{i}.*` indexés, `gen_ai.usage.prompt_tokens`/`completion_tokens`, `llm.usage.total_tokens`, `llm.request.type`, `llm.vendor`, `traceloop.span.kind` | LangChain / LangGraph / CrewAI instrumentés Traceloop épinglés **< openllmetry v0.55.0** (sorti le 2026-03-29). Les providers en majuscules (`OpenAI`, `Langchain`) sont mis en minuscules pour que les FinOps ne se scindent pas selon la casse |
| **events v1.36-ou-antérieur** (le nom propre de la spec) | `1.36.0` | `gen_ai.system` ; les cinq events de log par message `gen_ai.{system,user,assistant,tool}.message`, `gen_ai.choice` (reconnus **par nom** — leur unique attribut est optionnel) | spans LLM Google ADK (`gcp.vertex.agent`), AutoGen (`autogen`), Microsoft Agent Framework — tous émettent encore `gen_ai.system` |
| **messages v1.37+** (actuel) | `1.41.1` | `gen_ai.provider.name`, `gen_ai.input.messages` / `gen_ai.output.messages` / `gen_ai.system_instructions`, l'event `gen_ai.client.inference.operation.details`, `gen_ai.workflow.name` | instrumentations officielles OTel ; openllmetry **≥ v0.55.0** |

Un signal ne portant que des clés dont les noms sont identiques d'une génération à l'autre
(p. ex. un span ADK `invoke_agent` : operation + agent + conversation, aucune clé provider)
est normalisé sous le pin actuel — le mapping appliqué est identique au byte près, et la
vraie release du producteur n'est pas déductible du wire.

#### Conventions MCP (`mcp.*`, semconv v1.39 — Development)

Exactement quatre attributs `mcp.*` existent en amont (`mcp.method.name`,
`mcp.protocol.version`, `mcp.resource.uri`, `mcp.session.id`) ; l'outil voyage sur
`gen_ai.tool.name` et le prompt sur `gen_ai.prompt.name`. L'ingest joint ces traces aux
propres faits de gouvernance MCP du produit en réutilisant les mêmes resource kinds
qu'émet le chemin Claude :

| Signal MCP | mappe vers |
|---|---|
| tout span `mcp.*` côté client avec `server.address` | arête session→`mcp.server` (rejoint les arêtes `claude_code.mcp_server_connection`) |
| `tools/call` + `gen_ai.tool.name` | arête d'accès `mcp.tool` (`server.address/tool` quand l'endpoint est connu) — le même kind que les invocations `mcp__server__tool` de Claude |
| `resources/read` / `resources/subscribe` + `mcp.resource.uri` | arête `mcp.resource` en **mode lecture** (URI nettoyée : credentials/query supprimés) |
| `prompts/get` + `gen_ai.prompt.name` | arête `mcp.prompt` en **mode lecture** (surface de prompt) |
| spans de kind SERVER / métriques `mcp.client|server.*.duration` | liveness uniquement (dégradation propre — la vue du serveur n'attribue aucune identité d'agent) |

#### Spans d'agent (split client/internal de `invoke_agent` + `invoke_workflow`, semconv v1.41 — Development)

v1.41.0 a scindé `invoke_agent` en une variante **CLIENT** (service d'agent distant) et une
variante **INTERNAL** (in-process). Les frameworks réels violent le kind aujourd'hui
(AutoGen et Microsoft Agent Framework codent en dur CLIENT pour des agents in-process ;
Google ADK utilise INTERNAL), aussi l'ingest ne classe une invocation comme **distante**
que lorsque le span est CLIENT **et** porte un `server.address` — ce qui produit une arête
de délégation conversation→`genai.agent.remote`. Tout le reste demeure une invocation
in-process couverte par l'arête d'attribution conversation→`genai.agent` : dégradé
proprement, jamais un « remote » fabriqué. `invoke_workflow` (nouveau en v1.41 ; équipes de
style CrewAI) mappe une arête conversation→`genai.workflow`. Les spans d'agent restent en
**Development** (expérimental) en amont — aucune stabilité n'est revendiquée.

**Stable vs expérimental, honnêtement :** le **mécanisme** (gate opt-in, détection de
dialecte + lectures dual-name, mapping span/event/métrique, les shapes scellées
`CostSample`/`EdgeObservation`) est stable dans ce produit. Le **vocabulaire** qu'il mappe
(clés `gen_ai.*`/`mcp.*`, l'enum d'opération) est en **Development** amont et pourra être
renommé à nouveau ; c'est exactement pourquoi l'ingest normalise chaque génération plutôt
que d'en épingler une. v1.41.1 est la dernière release *versionnée* des conventions gen-ai
(elles ont migré vers `open-telemetry/semantic-conventions-genai`, qui n'a aucune release
en juin 2026). Notes :

- **Le coût est dédupliqué par span id W3C.** Lorsqu'une opération rapporte un usage *à la
  fois* sur son span et sur son event `operation.details` (ils partagent un span id), il
  est facturé une fois, pas deux.
- **Les métriques alimentent la liveness, jamais le coût.** `gen_ai.client.token.usage` est
  un agrégat ; le span/event est l'usage par opération faisant autorité, si bien que
  facturer aussi la métrique double-compterait. Les histogrammes de durée `mcp.*` v1.39 sont
  reconnus de la même façon.
- **Le provider peut être `unknown`.** Si un span porte un modèle mais pas de
  provider/system, le coût est attribué à `unknown` plutôt que deviné depuis l'id du modèle.
- **Un total-only de tokens n'est pas scindé.** Le legacy `llm.usage.total_tokens` sans
  split prompt/completion n'est jamais deviné en entrée/sortie (pas de coût fabriqué).
- **OpenInference (Arize/Phoenix) est une convention différente** et n'est *pas* ingérée par
  ce profil — les clés `llm.*` lues ici (`llm.request.type`, `llm.usage.total_tokens`,
  `llm.vendor`) sont des **marqueurs legacy OpenLLMetry**, pas le namespace `llm.*`
  d'OpenInference.

## Cooperative — configuration de la surface de l'agent local

Ces sources lisent la configuration déclarée d'un agent local et émettent des arêtes
**permises**, ainsi que des findings de posture. Ce ne sont pas des traces d'exécution en
direct ; lorsqu'un framework dispose d'OTEL natif, l'usage live arrive toujours par
l'ingest `gen_ai.*` ci-dessus.

| Kind | Observe | Couverture honnête |
|---|---|---|
| `opencode` | Couches JSONC locales `opencode.json` / `opencode.jsonc` → posture des permissions, posture managed/admin-override, arêtes permises de MCP/outils/agents personnalisés, findings de credential-in-config/share/autoupdate/OTEL et fragment d'authoring | Configuration déclarée uniquement. La couche managed est détectée localement, mais ce n'est pas un verrou immuable : `OPENCODE_PERMISSION` au runtime, la redirection du répertoire de tests et la configuration distante de l'organisation restent hors de ce lecteur. OTEL natif, lorsqu'il est activé, peut alimenter l'usage live `gen_ai.*` via l'exporteur `OTEL_*` hors bande |
| `gemini-cli` | Couches `settings.json` Gemini CLI (système/utilisateur/workspace) → arêtes permises de MCP/outils, posture de lacune d'application et inventaire de configuration effective | Configuration déclarée uniquement ; l'usage live passe par l'ingest `gen_ai.*` (la CLI l'émet nativement). Ce n'est pas l'API Gemini (qui relève de la surface de fournisseur hébergé) |
| `openhands` | `config.toml` + environnement OpenHands → posture de sandbox/épinglage de modèle/credential/télémétrie et arêtes permises de MCP/actions | Configuration déclarée uniquement ; usage live via OTEL `gen_ai.*` natif |
| `goose` | `profiles.yaml` + environnement Goose (Block) → posture des réglages admin/de l'épinglage de modèle/des extensions/de l'approbation des outils et arêtes permises d'extensions | Configuration déclarée uniquement |
| `cline` | Namespaces Cline / Kilo Code dans le `settings.json` VSCode → posture d'approbation automatique/allowlist MCP/credential/épinglage de modèle | Configuration déclarée uniquement ; pas d'OTEL natif en amont |
| `grok` | Grok Build (xAI) — l'agent de codage en terminal, lu depuis sa configuration LOCALE : câblage des hooks, événements avec veto documenté et posture de gouvernance déclarable | **Ce n'est PAS le connecteur de l'API xAI** (`xai` lit le catalogue et le coût, avec `grok-build-0.1` parmi ses MODÈLES). Celui-ci lit l'AGENT, et leurs périmètres ne se chevauchent pas. La moitié OBSERVATION passe par l'ingest OTLP que Grok Build émet déjà. Seul `PreToolUse`, l'unique événement au veto documenté, revendique `PostureEnforced` ; le reste est `observed` |
| `openclaw` | `openclaw.json` OpenClaw (découverte JSON5, `$include` confiné) → posture gateway/canal/outil/sandbox/skill/modèle par agent et arêtes déclarées de canal/skill/modèle | Configuration déclarée uniquement ; aucun hook PEP inline vérifié en amont |
| `hermes` | `config.yaml` + arbres de profils + scope managed Hermes Agent → posture terminal/canal/skill/sécurité/modèle/MCP et arêtes déclarées | Configuration déclarée uniquement ; aucun hook PEP inline ni OTEL natif vérifié en amont |
| `google-adk` | JSON de session Google ADK 2.0 exporté → inventaire agent/app, sous-agents, appels de fonctions d'outils, transferts, drift des outils approuvés et corrélation Vertex reasoningEngine | Export en lecture seule ; jamais le contenu des messages. Distinct de la surface de plateforme `google-agent` |
| `agents-md` | Parcours du dépôt des fichiers d'instructions d'agents (AGENTS.md et fichiers de mémoire/instructions par agent) → drift de baseline SHA-256 + scan d'injection d'instructions / Unicode masqué / secrets | Données minimales : chemins assainis + détails hachés, jamais le contenu |
| `mcpb` | Extensions desktop `.mcpb` installées / distribuées → scan de posture du manifeste, drift de l'allowlist enterprise et vérification de signature PKCS#7 | PERMIS-vs-OBSERVÉ sur la surface d'extensions |
| `codex-managed-config` | Fichiers managed-config OpenAI Codex → posture d'application + drift par rapport à la baseline rédigée | Observation uniquement : il ne peut pas empêcher un développeur de contourner la couche managed (le pendant de `managed-settings` pour Codex) |

## Clean — audit natif du store (lecture/écriture telle quelle)

Ces connecteurs lisent la **propre** piste d'audit d'un store et reprennent la
classification lecture/écriture telle quelle — jamais inférée du texte de la requête.
`pgaudit` et `s3cloudtrail` sont les sources R/RW canoniques autour desquelles l'[access
map](/fr/reference/modules/iii-access-map/) est construite (leurs alias avec tiret `pg-audit`
/ `s3-cloudtrail` résolvent aussi).

| Kind | Observe |
|---|---|
| `pgaudit` | Piste **pgAudit** PostgreSQL (csvlog/jsonlog) → accès R/RW aux tables, `READ`/`WRITE` tels quels depuis la CLASS de pgAudit |
| `s3cloudtrail` | Events S3 d'AWS **CloudTrail** → R/RW d'objets, lecture/écriture depuis le flag `readOnly` de CloudTrail (fait aussi remonter les invocations de modèles Claude-on-Bedrock) |
| `snowflake-audit` | Historique d'accès natif Snowflake |
| `databricks-uc` | Audit Databricks Unity Catalog |
| `bigquery-audit` | Audit d'accès aux données BigQuery |
| `redshift-audit` | Audit Amazon Redshift |
| `mssql-audit` | Audit SQL Server |
| `oracle-audit` | Audit unifié Oracle |
| `gcs-audit` | Audit d'accès aux données Google Cloud Storage |
| `azure-blob-audit` | Audit Azure Blob Storage |

## Management plane cloud — inventaire org/tenant + activité du control plane

La parité tri-cloud pour le plane de **management** — distinct du plane de **données** par
ressource que couvrent les connecteurs d'audit de store ci-dessus. Chacun est un client API
vivant, **en lecture seule**, du control plane org/tenant d'un cloud : il découvre la
**topologie** des ressources (arêtes d'inventaire, `mode=unknown`, attributed) et lit le
**flux d'audit** natif du cloud pour l'**activité** du control plane (arêtes
`identity→…api`, classées lecture/écriture). Ils complètent la matrice qu'AWS ancre déjà
avec `s3cloudtrail` (plane de données) plus le connecteur `aws` IAM/CloudTrail au niveau du
compte. Les deux s'exécutent **in-process** et sont **offline-safe** (pas de credential ⇒
Gather est un no-op) ; les deux n'observent que le control plane — jamais un payload, un
secret, une clé ou une propriété de ressource.

| Kind | Observe | Couverture honnête |
|---|---|---|
| `gcp-audit` | GCP **Resource Manager / IAM** (topologie org→folder→project→service-account) + **Cloud Audit Logs** (Admin Activity + Data Access) → `identity→gcp.api` | **Clean** là où c'est journalisé : Admin Activity est une écriture par définition du type de log, Data Access est lecture/écriture d'après le verbe de méthode standard. **Lossy** là où la journalisation Data Access est désactivée (off par défaut dans GCP) ou qu'un verbe de méthode est non standard (`unknown`, jamais deviné). `approximate` pour les principals partagés déclarés ; le `principalEmail` converge avec le roster SPIFFE/SA |
| `azure-activity` | Azure **Resource Graph** (topologie tenant→subscription→resource) + **Azure Monitor Activity Log** (opérations du control plane) → `identity→azure.api` | **Clean** pour les écritures/suppressions du control plane (telles quelles depuis l'action RBAC). Le suffixe `action` générique est **lossy** (`unknown` — il peut lire ou écrire). Les **lectures** du plane de données ne sont **pas** dans l'Activity Log (le plane de données `azure-blob-audit` / `azurekeyvault` les couvre). `approximate` pour les appelants partagés ; l'`objectId`/`appId` de l'appelant converge avec le roster Entra |
| `cloudflare` | Estate edge Cloudflare — **Workers, buckets R2 et jobs Logpush** via l'API REST v4 → arêtes de topologie | Inventaire uniquement (aucun flux d'audit dans ce connecteur) ; jeton en lecture seule à scope limité. Distinct des surfaces IA `cloudflare-ai-gateway` / portails MCP |

L'opt-in **Data Access** de GCP et les trous **lecture-non-journalisée** d'Azure sont les
arêtes honnêtement **opaques** de ce plane : l'absence d'arête d'activité n'est pas une
preuve d'absence d'accès là où ces logs sont désactivés. La table complète de niveaux par
cloud se trouve dans le contrat livré du connecteur cloud-management
(`docs/contracts/S165-connectors-cloud-management.md`).

## Fournisseurs de modèles hébergés — catalogue, posture et mesure

Ces sources gouvernent les comptes et catalogues des fournisseurs de modèles hébergés.
Elles ne font **pas** proxy de l'inférence ; lorsqu'un fournisseur ne propose pas d'API
d'usage exploitable, la dépense est estimée par le Meter du connecteur autour du chemin
d'inférence plutôt que tirée d'un flux de facturation agrégé.

| Kind | Observe | Couverture honnête |
|---|---|---|
| `openai` | Usage et coût de la plateforme OpenAI (API d'organisation), plus catalogue des modèles et clés API | Clé org/admin en lecture seule ; aucun payload du data plane. Distinct de `azure-openai`, qui utilise les véritables surfaces Azure et non les chemins d'organisation OpenAI |
| `gemini` | Catalogue de modèles hébergés Gemini (Google) et export d'usage câblé par l'opérateur | Surface du fournisseur hébergé. Distincte de `gemini-cli`, qui observe les réglages locaux de la CLI, et de `vertex`, qui couvre les surfaces Vertex enterprise. Google n'expose aucune API d'usage agrégé sur ce chemin ; l'usage est donc celui que câble l'opérateur |
| `deepseek` | Catalogue hébergé DeepSeek, disponibilité du solde du compte et posture de souveraineté PRC | Aucune API d'usage agrégé ; coût mesuré autour de l'inférence à partir du tarif déclaré |
| `mistral` | Catalogue Mistral et posture de gouvernance | Aucune API publique d'usage/facturation/plafond de dépense ; coût mesuré autour de l'inférence à partir du prix catalogue |
| `xai` | Catalogue xAI/Grok en direct, endpoints de facturation, inventaire clés/ACL et posture de crédit et de plafond de dépense | Utilise les endpoints de gestion de facturation en lecture seule pour le coût ; les credentials de gestion et d'inférence sont distincts |
| `glm` | Catalogue déclaré Zhipu GLM / Z.ai, Meter de prix catalogue en USD, sonde d'entitlement et posture de souveraineté | Catalogue uniquement + Meter : GLM n'expose aucune API vérifiée d'usage, facturation, solde, admin, clés ou organisation. La réserve liée à la PRC / Entity List s'applique aux surfaces `z.ai` et `bigmodel.cn` |
| `vertex` | Catalogue Google Vertex AI, usage de tokens par modèle (Cloud Monitoring), coût facturé opt-in (export de facturation) et posture de sûreté Model Armor opt-in | Surface Google enterprise que le chemin AI Studio ne couvre pas ; GCP n'a pas d'API de coût en temps réel |
| `azure-openai` | Déploiements + modèles Azure OpenAI / AI Foundry (ARM), usage des tokens Azure Monitor et surfaces de coût | Client du management plane en lecture seule ; aucun payload du data plane |
| `openrouter` | Catalogue OpenRouter en direct (prix USD/MTok), posture d'usage/limite du compte et drift de politique des modèles approuvés | Coût facturé via le `MeterCall` exporté ; no-op hors ligne |
| `cohere` | Catalogue de modèles Cohere en direct (Models API paginée par curseur) | Aucune API publique d'usage/facturation/org (dashboard uniquement) — réserve de couverture honnête ; coût mesuré autour de l'inférence à partir du prix catalogue |
| `fal` | Inventaire du cycle de vie des clés API fal.ai + posture de rotation ; coût mesuré autour de l'API de file d'attente | Aucune API publique d'usage/audit — gouvernance par le cycle de vie des clés ; les surfaces profondes sont soumises à la vente et marquées UNVERIFIED |

## Inférence auto-hébergée — catalogues et usage locaux

L'inférence auto-hébergée est toujours dans le périmètre ; c'est donc une source de
première classe plutôt qu'une réflexion tardive du gateway. Ce niveau observe ce qu'un
runtime local sert réellement.

| Kind | Observe | Couverture honnête |
|---|---|---|
| `local` | Catalogue de modèles Ollama (`/api/tags`), **résidence Ollama (`/api/ps`)** — modèles chargés à cet instant, répartition GPU/CPU et échéance de déchargement — et usage de tokens vLLM sur sa surface compatible OpenAI | La résidence est signalée comme posture, et sa gravité est le PLACEMENT : un modèle entièrement en VRAM est informatif, tandis qu'un modèle résidant sur CPU ou RÉPARTI entre CPU et GPU est signalé, car c'est le cas où l'opérateur paie de la latence sans en être informé. Ollama ne publie aucune métrique agrégée de tokens, donc n'apporte aucune mesure. Cette source ne fournit toujours ni identité ni politique par appel sur l'inférence locale ; les gouverner exige le chemin gateway ou OTel. Ollama sur localhost ne requiert aucun credential : une config vide constitue donc un défaut fonctionnel en lecture seule ; désactiver un serveur exige une URL vide EXPLICITE, et les deux vides donnent un no-op |

## Backstop kernel — eBPF / Tetragon (signal clean, attribution approximate)

La moitié **non coopérative** du moat : là où le chemin coopératif voit ce qu'un agent
*rapporte*, celui-ci voit ce que le noyau *a fait* — lectures/écritures de fichiers et
connexions sortantes — même quand un agent désactive sa propre télémétrie. L'**accès** est
une vérité-terrain noyau (un signal de niveau clean de *ce qui s'est passé*) ;
l'**attribution** est délibérément honnête sur sa limite — le noyau attribue à une identité
runtime (process/cgroup/conteneur), jamais à un agent résolu, si bien que chaque arête eBPF
est `approximate`. Il ne déchiffre ni n'inspecte jamais les payloads (il est aveugle au
corps TLS).

| Kind | Observe | Limite honnête |
|---|---|---|
| `ebpf` | Events noyau Tetragon → R/RW de fichiers (masque `MAY_*`) et arêtes réseau ; finding anti-évasion optionnel quand un agent agit au niveau du noyau sans télémétrie coopérative | Anonyme côté agent → toujours `approximate` ; un backstop en flux, pas un ledger per-agent |

Il **ne** charge **pas** lui-même de programmes eBPF : la capture noyau est faite par
[Tetragon](https://tetragon.io/) (un DaemonSet séparé et durci). Voir
[Exigences de déploiement](#exigences-de-déploiement-et-attribution-honnête).

## Lossy — les arêtes apparaissent, souvent approximate

| Kind | Observe | Pourquoi lossy |
|---|---|---|
| `mongo-audit` | Audit MongoDB | Document-store ; la séparation des appelants est faible |
| `openlineage` | Events de run OpenLineage → lineage de datasets | Le lineage n'est pas un audit par appel |
| `delta-sharing` | Activité des destinataires Delta Sharing | Attribution destinataire-partagé |

## Sources approximate-by-attribution et côté permis

Celles-ci émettent soit le côté **permis** (grants déclarés), soit des accès attribués à un
rôle / processus / credential partagée plutôt qu'à un agent résolu.

| Kind | Observe | Niveau |
|---|---|---|
| `iceberg-catalog` | Catalogue REST Iceberg → grants permis + identités de credentials délivrées | permitted |
| `inference-gateway` | Routage K8s Gateway API Inference-Extension → routes d'inférence permises | permitted |
| `aws-kms` / `gcp-kms` / `azure-key-vault` | Audit cloud KMS → arêtes d'accès aux clés (jamais le matériel de clé) | approximate |
| `external-secrets` / `sops` / `kmip` | Manifests de gestion de secrets / locate KMIP → arêtes de provisioning/custody | approximate (existence, pas usage) |
| `istio-telemetry` | CRDs Istio Telemetry → arêtes de mesh L7 | approximate (CRDs parsés, pas flux en direct) |
| `egress-proxy` | Journal de verdicts du proxy d'egress → arêtes d'egress L7 | approximate |
| `kong-audit` | Journaux d'audit Kong → findings de changement de configuration | approximate |
| `ai-gateway` | Enregistrements d'usage Envoy AI Gateway → échantillons de **coût** (FinOps) | flux de coût |
| `github` | Dépôts GitHub comme sources de données d'agents → arêtes d'accès R/RW observées (webhook d'abord, réconciliation par polling API) + arêtes ACL permises | observé + permis ; streaming (`poll_seconds: 0`) |
| `gitlab` | Dépôts GitLab → arêtes d'accès R/RW observées + arêtes ACL permises | observé + permis ; streaming (`poll_seconds: 0`) |

## Observateurs de posture — findings, pas arêtes d'accès

Des observateurs read-first qui font remonter la posture (sync/santé/drift, anomalies
d'auth) sous forme de findings ; ils ne mutent jamais l'estate.

| Kind | Observe |
|---|---|
| `runtime` | Où s'exécutent les charges AI (Linux procfs, daemon Docker, API Kubernetes) → arêtes de containment + findings de santé (nécessite un accès hôte — voir [Exigences de déploiement](#exigences-de-déploiement-et-attribution-honnête)) |
| `argocd` / `flux` / `crossplane` | CRDs GitOps / control plane → posture de sync, santé, drift, composition |
| `kerberos` | Télémétrie d'auth KDC → findings de Kerberoasting |
| `aaa` | Observations AAA RADIUS / TACACS+ |
| `ssf` | Récepteur Shared-Signals / CAEP (kill-switch d'agent) |
| `edugain` / `openidfed` | Agrégat de fédération / chaînes de confiance OpenID-Federation → posture de fédération |
| `managed-settings` | Politique Claude `managed-settings` → arêtes permises + findings de drift |
| `envoy-ai-gateway` | Export de **configuration déclarée** Envoy AI Gateway → posture gateway + drift de politique gateway-vs-Olivares (le pendant de configuration du flux d'usage `ai-gateway`) |
| `kong-agent-gateway` | Export de configuration déclarée Kong agent-gateway → posture + drift de politique |
| `litellm` | Export de configuration déclarée du proxy LiteLLM → posture + drift de politique |
| `bedrock-kb` | Santé/configuration de récupération Amazon Bedrock Knowledge Bases (health-check Retrieve d'Agent Runtime) → findings de posture par KB + arêtes KB→source de données. Jamais `RetrieveAndGenerate` (aucune inférence facturable), jamais le contenu complet d'un document |
| `tak` | Posture du `CoreConfig.xml` TAK Server (+ sonde mTLS facultative) et ingest Cursor-on-Target gouverné et minimal-data (positions condensées, uid haché) |
| `a2a` | Pairs Agent2Agent (A2A) v1.0 → découverte d'Agent Card + vérification de signature JWS/JCS (niveau de confiance du pair) et interactions tâche/message observées comme arêtes agent↔agent. Observation uniquement — ne distribue jamais une tâche ; l'émission de cards signées est une capacité distincte |

## Untrusted hint — introspection MCP

La source `mcp` introspecte les serveurs MCP (stdio + Streamable HTTP) et émet des **arêtes
de capacité** portant les hints R/RW *déclarés* par le serveur, plus des findings de
révision de protocole, de surface de fonctionnalités et de provenance de registry. Selon la
spécification MCP, une annotation d'outil est une déclaration **non fiable** — une
*revendication* de capacité, corroborée contre une source observée, **jamais accordée
seule**. (La source coopérative `claude` introspecte aussi MCP dans le cadre de son chemin
OTLP ; `mcp` est l'introspecteur autonome que vous pointez vers une liste de serveurs ou un
`.mcp.json`.)

| Kind | Observe | Niveau |
|---|---|---|
| `mcp` | Outils/ressources/prompts de serveurs MCP → arêtes de capacité déclarée + findings de posture | untrusted hint |

## Observateurs de brokers et meshes hors processus

Ceux-ci portent des arbres de dépendances de protocole de transport lourds, chacun
s'exécute donc **hors processus** (la dépendance ne s'édite jamais dans le cœur). Un
connecteur atteint de nombreuses cibles.

| Kind | Observe |
|---|---|
| `kafka` | Activité de topics Kafka / Event Hubs / Redpanda / MSK |
| `amqp` | Brokers AMQP (RabbitMQ, Azure Service Bus) |
| `nats` / `mqtt` / `cloudqueue` | Activité NATS, MQTT, cloud queue |
| `debezium` | Flux de change-data-capture Debezium |
| `envoy` | Services d'observation Envoy ALS / ext_authz / ext_proc |
| `hubble` | Données de flux Cilium Hubble |

## Fournisseurs de roster d'identités

Ceux-ci peuplent le **roster** d'identités non-humaines qui affine l'attribution
(transformant les arêtes `approximate` en `attributed`). Chaque source avec une surface de
grant émet aussi ses arêtes d'**accès permis** (`SignalPolicy`) depuis `Gather` — le côté
PERMITTED du diff permitted-vs-observed :

| Kind | Roster | Arêtes permises |
|---|---|---|
| `vault` | entités, groupes, politiques | grants de chemin par politique ACL (`vault.path`), étendus par entité liée |
| `ldap` | utilisateurs, comptes service/computer, groupes | appartenance à un groupe privilégié → grants d'annuaire (`ldap.directory`) |
| `idp` (Okta / Entra) | utilisateurs, apps/service principals, groupes | grants d'app-assignment / scope (`okta.app` / `entra.app`) |
| `infisical` | identités machine, membres d'org, projets | grants de projet (`infisical.project`) |
| `keycloak` | realms, clients, rôles, groupes, utilisateurs | roster uniquement (`Gather` no-op) |
| `pingone` / `forgerock` | Rosters d'annuaire PingOne / ForgeRock via le même lecteur multi-provider (le kind initialise le `provider` correspondant ; `ping` est un alias de `pingone`) | roster uniquement (`Gather` no-op) |
| `spiffe` | entrées d'enregistrement SPIRE | roster uniquement (`Gather` no-op) |

Câblez `as_source: true` sur l'entrée `identity` pour une passe unique de grants permis par
boot, ou une entrée `sources` séparée avec `poll_seconds` pour des re-scans périodiques —
jamais les deux pour un même kind (`okta`/`entra` partagent le même connecteur `idp`, donc
une seule instance de la famille idp peut s'enregistrer comme source par processus). Les
appartenances aux groupes/rôles ne voyagent que dans le snapshot typé du roster, jamais
sous forme d'arêtes.

### Fédération d'identité d'agents

Les **registries d'agents** des hyperscalers fédèrent en lecture seule contre le roster
SPIFFE/WIF du plane. Leurs lignes per-agent (kinds `agent_identity` / `workload_identity`)
sont des identités dédiées, non partagées, si bien que l'access map les traite comme une
attribution per-agent **ferme** ; les lignes annexes des mêmes sources (principals de
blueprint, fournisseurs de credentials, agents adossés à un service account) restent
approximate. La fédération n'écrit jamais dans un registry ; l'*export* vers les control
towers est une capacité distincte et ultérieure.

| Kind | Fédère | Gather |
|---|---|---|
| `entra-agent` | Microsoft Entra Agent ID (identités d'agent, utilisateurs d'agent, blueprints, principals de blueprint, owners/sponsors, calcul des orphelins in-snapshot, soft-deleted opt-in) via Graph v1.0 | findings de drift `nhi_longlived_credential`, findings de posture CA/agent risqué/gouvernance/sans sponsor et arêtes d'accès d'agents observées via `auditLogs/signIns` bêta opt-in — ajoutez une entrée `sources` avec `poll_seconds` |
| `agentcore` | AWS Bedrock AgentCore Identity (workload identities, fournisseurs de credentials token-vault) + moteurs AgentCore Policy/politiques Cedar comme collections | findings de drift `nhi_longlived_credential` (fournisseurs d'API-key statiques) — ajoutez une entrée `sources` avec `poll_seconds` |
| `google-agent` | Google Agent Identity (reasoning engines Agent Runtime ; identités d'agent basées sur SPIFFE), plus posture Agent Registry / Agent Gateway. Les lignes utilisent l'**ID SPIFFE complet** comme ref, convergeant avec le roster `spiffe` ; Gather détecte les agents de registry non attribués, les reasoning engines fantômes hors d'un registry lisible, les annotations risquées d'outils MCP et la posture du registry gateway | findings de posture registry/gateway et détection d'agents fantômes — ajoutez une entrée `sources` avec `poll_seconds` |
| `agent365` | Registry Microsoft Agent 365 (inventaire au niveau package, agents *sans* identité Entra inclus) via Graph v1.0, client credentials avec permissions d'application ou jeton délégué, détails de package opt-in | findings d'hygiène du registry (packages déployés bloqués ; packages externes/partagés déployés à tous les utilisateurs) — ajoutez une entrée `sources` avec `poll_seconds` |
| `foundry-agents` | Projets Microsoft Foundry, applications/déploiements d'agents et agents Agent Service actuels via ARM + Foundry Agent Service v1 ; corrèle les liens d'identité d'app avec `entra-agent` | findings de posture applicative dérivés d'ARM (identité d'agent Entra manquante ; déploiement en échec sur une app activée) — ajoutez une entrée `sources` avec `poll_seconds` |
| `ai-control-tower` | Inventaire d'actifs numériques ServiceNow AI Control Tower (Table API, lecture seule) | no-op (roster uniquement) |
| `oasf` | Descripteurs d'agent AGNTCY/OASF + vérification Agent Badge — **EXPÉRIMENTAL** jusqu'à ce que la spec d'identité soit conforme VCDM 2.0 | findings de badge — ajoutez une entrée `sources` avec `poll_seconds` |
| `onepassword` | Compte 1Password comme custodian `secret_store` | arêtes d'accès-secret item-usage — ajoutez une entrée `sources` avec `poll_seconds` |

Pour les sept kinds dotés d'un Gather re-pollable (`entra-agent`, `agent365`, `agentcore`,
`foundry-agents`, `google-agent`, `oasf`, `onepassword`), câblez la moitié **roster** comme une entrée `identity` *sans* `as_source`
et la moitié **arêtes/findings** comme une entrée `sources` séparée avec `poll_seconds` —
pas les deux via `as_source: true`, qui n'exécute le scan qu'une seule fois par boot (et un
enregistrement en double du même kind est rejeté).

Les **owner/sponsor** déclarés par le registry atterrissent sur les enregistrements de
cycle de vie NHI pendant le sync du roster (la même sémantique que `PUT
/nhi/{ref}/ownership`), et un **orphelin** asserté par le registry (un agent Entra dont le
blueprint a disparu) atterrit sur le flag `registry_orphaned` du même enregistrement — le
balayage de cycle de vie l'OR-e dans `orphaned` et émet le finding `nhi_orphaned`, si bien
que la détection d'orphelins surveille les agents fédérés sans aucun câblage
supplémentaire. La *source* `vault-audit` (sous `sources`, pas `identity`) suit le device
d'audit fichier de Vault et émet la contrepartie OBSERVED des grants permis de `vault` pour
les mêmes refs `entity:<name>`.

## Sources de documents de connaissance (pas de couverture d'access-map)

Celles-ci alimentent le module **knowledge** (module VIII), **pas** l'access map : elles
ingèrent du *contenu de document* pour une récupération gouvernée, n'émettent **aucune**
arête R/RW et ne produisent **aucune** observation sur le bus. Le module les *tire* (List →
Fetch) sur une requête d'ingest (`POST /v1/m/knowledge/kbs/{id}/ingest
{"source":"<name>"}`), elles sont donc câblées dans ce module — nommez-les sous `documents`
dans `OLIVARES_SOURCES_CONFIG`, pas `sources`. Chacune est en lecture seule et minimal-data :
elle porte l'ACL et la provenance de la source (jamais un email personnel ; le module
expurge le corps avant de persister).

| Kind | Ingère |
|---|---|
| `gdrive` | Documents Google Drive (Docs/Sheets/Slides/fichiers) |
| `confluence` | Espaces et pages Atlassian Confluence |
| `notion` | Workspaces, bases de données et pages Notion |
| `sharepoint` | Sites et documents Microsoft SharePoint / OneDrive |
| `s3content` | Contenu d'object-storage (objets S3 / R2 / GCS) |
| `sap_odata` | Entités de service SAP OData sous forme de documents gouvernés |
| `salesforce` | Objets/enregistrements Salesforce sous forme de documents gouvernés |
| `snowflake` | Tables/lignes Snowflake sous forme de documents gouvernés (distinct de l'observateur R/RW `snowflake-audit`) |
| `azure_ai_search` | Documents d'index Azure AI Search |
| `postgres` | Lignes PostgreSQL sous forme de documents gouvernés — lecture seule par construction, ACL déclarée par ligne, classification par colonne (distinct de l'observateur R/RW `pgaudit` ; pas de NL-to-SQL). Voir [Postgres comme source de contexte gouvernée](/fr/how-to/govern-postgres-content/). |
| `filesystem` | Contenu de serveur de fichiers (local / NFS / SMB) — lecture confinée à la racine par construction, propriétaire/groupe/ACL POSIX transposés vers les ACL de documents, classification xattr (distinct du sink de logs `filelog`). Voir [Gouverner votre serveur de fichiers](/fr/how-to/govern-your-file-server/). |

```jsonc
// OLIVARES_SOURCES_CONFIG — document sources live under "documents", never "sources"
{
  "documents": [
    { "name": "eng-wiki", "kind": "confluence",
      "config": { "export_path": "/var/lib/olivares/confluence" } }
  ]
}
```

## Destinations de sortie (pas de couverture)

Les connecteurs de sortie **livrent** findings et notifications ; ils n'observent rien et
n'ont pas de niveau de couverture. Ils sont câblés séparément des sources.

Kinds de destination in-process : `slack`, `teams`, `pagerduty`, `opsgenie`, `webhook`,
`siem`, `splunkhec`, `syslog`, `servicenow`, `jira`, `email`, `twilio`, `chronicle`,
`datadog`, `elastic`, `snmp`, `filelog`, `otlplog` (logs OTLP/HTTP) et `s3archive`
(le sink WORM S3 Object Lock — un objet immuable dont le verrou est vérifié par
notification).

Trois kinds d'egress vers des brokers s'exécutent **hors processus** sous forme de plugins
embarqués (leurs arbres de dépendances de protocole wire ne se lient jamais au moteur,
exactement comme les sources plugin) : `kafka`, `amqp` et `cloudqueue` — les mêmes noms de
kind que leurs jumeaux sources ; comme destination, chacun livre la notification sous forme
de CloudEvent au broker/à la file configuré. Un simple build de développement sans
`task build:connectors` ignore une telle destination avec un avertissement honnête au boot,
au lieu de prétendre qu'elle existe.

:::note[Le webhook sortant est une destination, pas un webhook d'API]
`webhook` est un canal de sortie vers lequel le control plane pousse, pas un callback que
vous enregistrez contre l'API REST du produit — le document OpenAPI ne définit aucun
`webhooks`. Voir [Honnêteté & limites](/fr/start/honesty-and-limits/).
:::

## Exigences de déploiement et attribution honnête

Les connecteurs différentiels R/RW sont câblés dans le binaire par défaut, mais deux
comportent une **exigence de déploiement** que les autres n'ont pas — le code du connecteur
est agnostique vis-à-vis de l'hôte, les *données* qu'il consomme ne le sont pas :

- **`ebpf`** consomme l'export d'events noyau de [Tetragon](https://tetragon.io/). **Le
  connecteur n'a besoin d'aucune capacité noyau** — il lit un fichier/FIFO/`stdin` en
  `0600` que Tetragon possède (`events_path`, par défaut `-`). Tetragon lui-même est un
  **DaemonSet séparé et durci** détenant les `CAP_BPF` + `CAP_PERFMON` minimaux,
  s'exécutant non-root avec seccomp/AppArmor et sans listener entrant. Le déploiement est
  donc : exécuter Tetragon en privilégié (ses TracingPolicies file-access + TCP-connect
  intégrées), puis pointer `ebpf` vers son export. Tetragon minimum : v1.0.
- **`runtime`** lit le procfs de l'hôte (`proc_root`, par défaut `/proc`), le socket du
  daemon Docker (`docker_socket`, **off par défaut** — l'accès en lecture à `docker.sock`
  est root-équivalent ; opt-in délibéré, idéalement via un socket proxy en allowlist GET)
  et/ou l'API Kubernetes (ServiceAccount in-cluster par défaut). Ne montez que ce que vous
  activez.
- **`gcp-audit`** s'authentifie comme un service account GCP (key JSON ou un `access_token`
  émis par WIF/ADC) et n'a besoin que de rôles de **management en lecture seule** :
  `roles/resourcemanager.organizationViewer` + `roles/iam.serviceAccountViewer` +
  `roles/logging.viewer` — lire les entrées **Data Access** nécessite en plus
  `roles/logging.privateLogViewer`. Scopez `organization_id` (parcours d'org + audit
  org-scopé) et/ou `projects`. Les Data Access audit logs sont **off par défaut dans GCP** :
  activez-les via la config IAM/data-access, sinon le flux d'activité sous-rapporte
  honnêtement.
- **`azure-activity`** s'authentifie comme un service principal Entra (client-credentials)
  ou un `access_token` managed-identity, et n'a besoin que du rôle **Reader** à la racine du
  tenant (ou par subscription) — ce seul rôle couvre Resource Graph, le listing des
  subscriptions et l'Activity Log. Les subscriptions sont auto-listées quand `subscriptions`
  n'est pas défini.

Les deux s'exécutent quand même **in-process** (transport A) ; les binaires go-plugin
`cmd/{pg-audit,s3-cloudtrail,ebpf-source}` existent pour un déploiement **collector** hors
processus près de l'hôte si vous préférez les isoler là.

Chaque source est **opt-in, deny-closed** : un `log_path`/`path`/`events_path` manquant est
une erreur de configuration au démarrage (la source n'est pas câblée), jamais un no-op
silencieux. L'estate de démo ([quickstart](/fr/start/quickstart/)) ensemence des observations
synthétiques équivalentes à travers le vrai bus afin que vous puissiez voir le signal de
niveau clean de bout en bout avant de câbler une source réelle.

:::caution[Limites honnêtes sur chaque niveau]
- **L'absence d'arête n'est pas une preuve d'absence d'accès** là où la couverture est
  lossy, impossible, ou qu'une source n'est pas câblée. L'access map est honnête sur sa
  propre portée.
- **L'identité per-agent est la dépendance dure.** Un service account partagé derrière un
  pool de connexions fait retomber l'attribution à `approximate` sur un store de niveau
  clean lui-même — voir [gouverner et approuver](/fr/how-to/govern-and-approve/).
- **Les annotations d'outils MCP sont non fiables** selon la spécification MCP : un hint de
  capacité déclarée, corroboré contre une source observée, jamais accordé seul.
:::

## Voir aussi

- [Connecter une source](/fr/how-to/connect-a-source/) — le modèle de connecteur et comment en câbler un.
- [Connecter Claude Code](/fr/how-to/connect-claude-code/) — le chemin coopératif de bout en bout.
- [Module III — l'access map](/fr/reference/modules/iii-access-map/) — ce que deviennent les arêtes.
- [Honnêteté & limites](/fr/start/honesty-and-limits/) — le contrat d'honnêteté à l'échelle du produit.
