---
title: Gouverner Claude Code & Codex authentifiés par abonnement
description: >-
  Comment Olivares AI gouverne les agents de code qui s'authentifient avec un
  abonnement — Claude Code sur Pro/Max, Codex sur ChatGPT — sans jamais
  s'interposer au milieu de cet abonnement. Trois mécanismes (observer,
  managed-settings + hooks, une passerelle par clé d'API), une seule ligne
  rouge : nous ne routons jamais l'identifiant de votre abonnement.
sidebar:
  order: 6
---

L'agent le plus difficile à gouverner est celui auquel un développeur s'est
connecté avec un **abonnement** personnel ou d'entreprise : Claude Code connecté
avec Pro/Max, ou Codex connecté avec ChatGPT. La même configuration vaut pour Grok Build
et pour tout agent CLI qui authentifie la personne plutôt que la charge de travail : les
mécanismes ci-dessous concernent la *forme* de cette connexion, et non un fournisseur en
particulier. Il tourne sur un portable, il
s'authentifie avec un identifiant OAuth, et c'est exactement la surface qu'un
garde-fou de fournisseur cloud placé dans le chemin d'inférence ne voit jamais
(voir
[le coin enfoncé (the wedge)](/fr/explanation/positioning/where-olivares-fits-vs-your-gateway/)).
La « solution » tentante — placer devant lui un service qui détient l'abonnement
et route son trafic — est précisément celle qu'Olivares AI **ne** construira
**pas**, parce que les fournisseurs de modèles l'interdisent et parce qu'elle
ferait de notre control plane un point unique de compromission d'identifiant.

Cette page est le compte rendu honnête de la manière dont nous gouvernons ces
agents **sans jamais servir d'intermédiaire pour l'abonnement** : ce que nous observons, où nous
appliquons, et l'unique voie étroite où une passerelle est appropriée (et ce
n'est jamais celle de l'abonnement).

:::danger[La ligne rouge : nous ne routons jamais votre abonnement]
Olivares AI **ne détient, ne relaye et ne route jamais un identifiant
d'abonnement tiers.** La politique d'Anthropic elle-même l'énonce : *"Anthropic
does not permit third-party developers to offer Claude.ai login or to route
requests through Free, Pro, or Max plan credentials on behalf of their users"*
([Claude Code legal & compliance](https://code.claude.com/docs/en/legal-and-compliance),
récupéré le 2026-06-21 — l'interdiction nomme les trois plans grand public
**Free, Pro, Max**). Les conditions d'OpenAI fonctionnent de la même façon pour
une connexion ChatGPT/Codex grand public. Notre posture est plus stricte que la
ligne elle-même : nous ne routons **aucun** OAuth d'abonnement, de **quelque**
plan que ce soit. La gouvernance se passe *autour* de l'agent, jamais
*à l'intérieur* de son identifiant.
:::

## Pourquoi servir d'intermédiaire pour l'abonnement est exclu

Il vaut la peine d'être précis sur la règle, car le conseil juridique d'un
acheteur la vérifiera. La politique d'Anthropic dresse deux listes qu'il ne faut
pas confondre :

- **Qui peut utiliser OAuth tout court** — cinq plans : *"OAuth authentication is
  intended exclusively for purchasers of Claude Free, Pro, Max, Team, and
  Enterprise subscription plans and is designed to support ordinary use of Claude
  Code and other native Anthropic applications."*
- **Ce qu'un tiers ne peut pas faire** — router pour le compte d'utilisateurs :
  *"Anthropic does not permit third-party developers to offer Claude.ai login or
  to route requests through Free, Pro, or Max plan credentials on behalf of their
  users."*

L'interdiction nomme explicitement les plans **grand public** (Free, Pro, Max).
À l'inverse, la page n'accorde à personne la permission de router des sièges Team
ou Enterprise — elle reste silencieuse là-dessus, et nous ne lisons pas le silence
comme une licence. Pour les *développeurs construisant des outils*, les
recommandations d'Anthropic elles-mêmes détournent entièrement de l'OAuth
d'abonnement : *"Developers building products or services that interact with
Claude's capabilities, including those using the Agent SDK, should use API key
authentication through Claude Console or a supported cloud provider."*
([source](https://code.claude.com/docs/en/legal-and-compliance) ; répartition
plan-par-conditions : Team/Enterprise/API sous les Commercial Terms,
Free/Pro/Max sous les Consumer Terms.)

Notre connecteur Codex encode la discipline identique dans le code, par
conception : l'identifiant d'automatisation est une **clé d'API** OpenAI ou un
**jeton d'accès workspace**, jamais un abonnement ChatGPT personnel — *"proxying
it for third-party/programmatic use violates OpenAI's terms exactly as a consumer
Claude subscription does for Anthropic. There is no subscription config field by
design"* (`connectors/codex/codex.go`). La ligne rouge n'est donc pas une promesse
marketing rapportée après coup ; c'est la forme même du produit.

## Trois mécanismes, aucun d'eux n'étant l'abonnement

Nous gouvernons un agent authentifié par abonnement à travers trois canaux
indépendants. Les deux premiers ne touchent jamais l'inférence ; le troisième ne
la touche que pour le trafic qui s'authentifie avec une **clé d'API**, jamais un
abonnement.

### 1. Observer — télémétrie, usage et posture

Claude Code émet de l'OpenTelemetry, et un administrateur peut l'activer pour le
parc depuis le tier managé : *"Administrators can configure OpenTelemetry settings
for all users through the managed settings file"*
([Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)). Nous
ingérons ce **signal gen-ai** — sessions, tokens, coût, activité des outils — et
le transformons en carte d'accès et en constats de posture. Point crucial, c'est
**données minimales par construction du côté de Claude Code aussi** : le contenu
des prompts est *"redacted by default"* et les détails des outils, le contenu des
outils et les corps d'API bruts sont chacun *"(default: disabled)"* (même source).
Nous consommons l'usage et les métadonnées, pas les conversations.

Pour Codex, le même canal d'observation est l'ingestion par le connecteur des API
Analytics et Compliance/Audit — usage, adoption et enregistrements d'audit
immuables transformés en échantillons de coût et en preuves à altération détectable
(à altération détectable), portant *"never prompt/diff content or key values"*
(`connectors/codex/codex.go`).

→ [Ingérer OpenTelemetry GenAI](/fr/how-to/connectors/otel-genai/) ·
[OTel d'entreprise pour Claude Code](/fr/how-to/claude-code-enterprise-otel/)

### 2. Managed settings + hooks — le PEP in-process

L'observation n'est pas l'application. Le canal d'application pour Claude Code est
son fichier de **managed settings** au tier de politique de l'OS, qui porte un
hook `PreToolUse` non surchargeable qui rappelle le point de décision Olivares
avant l'exécution de chaque outil. Anthropic documente la propriété sur laquelle
nous nous appuyons : *"Environment variables defined in the managed settings file
have high precedence and cannot be overridden by users"*, et les managed settings
*"can be distributed via MDM"*
([monitoring](https://code.claude.com/docs/en/monitoring-usage)).

Olivares produit ce fichier (`olivares agent managed-settings`) avec
`allowManagedHooksOnly` de sorte que le hook propre d'un développeur ne puisse
jamais précéder ni court-circuiter celui qui est gouverné, et l'endpoint et le
porteur (bearer) par session sont injectés au lancement — non écrits dans le
fichier statique. La décision elle-même est **deny-closed à chaque arête** : un
appel d'outil n'est autorisé que lorsqu'une identité ferme se résout, que la
disposition de politique n'est pas `deny`, que le moteur de politique en direct ne
l'interdit pas, et — pour un `ask` — qu'une approbation humaine est liée au hash
de plan exact. Un arrêt d'urgence
([kill switch](/fr/reference/glossary/#kill-switch)) prime sur tout, y compris sur
une autorisation break-glass active.

C'est le mécanisme que la page
[PEP par hooks Claude Code](/fr/how-to/connectors/claude-code-hooks-pep/) documente
opérationnellement, et c'est ce qui nous rend capables de *gouverner* l'agent de
développement local, et pas seulement de le surveiller — le deuxième des
[trois axes](/fr/explanation/positioning/analyst-vocabulary/#les-trois-axes-que-ce-vocabulaire-désigne).

### 3. Passerelle pour une clé d'API — jamais pour OAuth

Il existe exactement une voie où Olivares se situe dans la ligne des requêtes
d'inférence, et elle n'existe que pour les appelants qui **n'utilisent pas** le
canal managed-settings de Claude Code : du trafic SDK brut ou `curl` authentifié
avec une **clé d'API** (ou un équivalent Bedrock/Vertex). Claude Code route de
telles requêtes avec `ANTHROPIC_BASE_URL` — *"To route requests through a custom
API endpoint, set the `ANTHROPIC_BASE_URL` environment variable instead"* — et
authentifie une passerelle avec un porteur via `ANTHROPIC_AUTH_TOKEN`, *"when
routing through an LLM gateway or proxy that authenticates with bearer tokens
rather than Anthropic API keys"*
([Claude Code IAM](https://code.claude.com/docs/en/iam)). Pointé vers le proxy
d'inférence inline d'Olivares, ce trafic obtient un pipeline gouverné — résidence,
accès aux modèles, fenêtre de contexte, DLP, budget, enregistrement — avant d'être
relayé.

La frontière est absolue : **cette voie porte du trafic clé d'API / porteur,
jamais l'identifiant OAuth d'un abonnement.** C'est la couture d'application pour
les appelants SDK/`curl` que les managed settings ne peuvent atteindre, et rien de
plus.

## L'encadré d'honnêteté : déploiement vérifié, pas inviolable

:::caution[Une application dont nous pouvons prouver qu'elle est *déployée*, pas une application qui *ne peut pas* être contournée]
Le PEP managed-settings + hook est **deny-closed** et **non surchargeable par
l'utilisateur via les settings** — mais ce n'est pas de la magie. Un développeur
qui pointe `ANTHROPIC_BASE_URL` vers son propre endpoint envoie l'inférence
entièrement ailleurs ; notre propre note d'ingénierie le dit clairement : *"a
custom `ANTHROPIC_BASE_URL` bypasses server-managed-settings entirely"*
(`modules/inferenceproxy/doc.go`). Nous ne prétendons donc jamais que le PEP est
impossible à échapper. Nous affirmons plutôt deux choses que nous pouvons tenir :

1. **Il est à déploiement vérifié.** Olivares atteste que les managed settings et
   le hook PEP sont effectivement présents sur l'hôte — un hôte non provisionné
   tourne non-gouverné-mais-observé, et cela est visible, pas caché.
2. **Le contournement est lui-même un constat.** Un `ANTHROPIC_BASE_URL` non par
   défaut sur un hôte remonte comme un constat de posture, et un environnement
   managé qui épingle une base URL divergeant de la passerelle Olivares autorisée
   soulève un constat de **dérive (drift)**
   (`connectors/claude-config`, `connectors/managedsettings`). L'évasion ne se
   tait pas ; elle s'allume.

« Déploiement vérifié, évasion-comme-constat » est l'histoire d'application
honnête pour tout agent qui tourne sur une machine que le développeur contrôle.
Nous ne vous vendrons pas de l'« inviolable ».
:::

## L'asymétrie de Codex, énoncée honnêtement

Claude Code et Codex ne sont pas symétriques, et la différence compte. Pour Codex
authentifié par ChatGPT, il n'y a **aucun équivalent documenté
d'`ANTHROPIC_BASE_URL`** — la
[page de configuration managée](https://developers.openai.com/codex/enterprise/managed-configuration)
d'OpenAI ne documente aucun setting ni variable d'environnement pour router
l'inférence à travers une base URL ou une passerelle personnalisée (vérifié par
récupération, 2026-06-21 ; une absence sur cette page, non une preuve qu'aucun
n'existe ailleurs). Nous **ne** gouvernons donc **pas** Codex en interceptant son
inférence.

Nous le gouvernons plutôt là où OpenAI *donne bel et bien* aux administrateurs des
contrôles appliqués. La configuration managée de Codex permet à une entreprise de
définir des *"Requirements: admin-enforced constraints that users can't override"*
qui *"constrain security-sensitive settings (approval policy, approvers reviewer,
automatic review policy, sandbox mode, permission profiles, web search mode,
managed hooks, and optionally which MCP servers users can enable)"* (même source).
Olivares rédige et atteste ces exigences (`connectors/codex-managed-config`) —
politique d'approbation, mode sandbox, l'allowlist MCP, télémétrie caviardée
(`log_user_prompt = false`) — et ingère les preuves Analytics et Compliance de
Codex. Gouvernance par la configuration et la preuve, non par un intermédiaire
(man-in-the-middle) sur l'appel de modèle.

## En un tableau

| Canal | Ce qu'il fait | Touche l'inférence ? | L'identifiant |
|---|---|---|---|
| **Observer** | Usage, coût, activité des outils → carte d'accès + posture ; Analytics/Compliance Codex → registre | Non | Aucun — télémétrie seulement, contenu caviardé par défaut |
| **Managed settings + hooks** | PEP `PreToolUse` deny-closed sur Claude Code, non surchargeable via les settings | Non | Celui de l'agent ; nous ne le voyons jamais |
| **Passerelle (clé d'API seulement)** | Pipeline gouverné pour les appelants SDK/`curl` bruts via `ANTHROPIC_BASE_URL` | Oui | **Clé d'API / porteur — jamais l'OAuth d'abonnement** |
| **Codex managed-config** | Exigences appliquées par l'admin (approbation/sandbox/MCP) + ingestion de preuves | Non | Celui de l'org ; configuration, non interception |

## En lien

- [Où Olivares s'inscrit face à votre passerelle / Guardrails](/fr/explanation/positioning/where-olivares-fits-vs-your-gateway/)
  — pourquoi rien de tout cela ne concurrence votre passerelle IA.
- [Olivares AI face à WitnessAI](/fr/explanation/positioning/vs-witnessai/) — le
  face-à-face sur la gouvernance des agents dans les IDE.
- [Hooks Claude Code & le PEP](/fr/how-to/connectors/claude-code-hooks-pep/) et
  [Exécuter Claude Code avec Olivares](/fr/how-to/run-claude-code-with-olivares/) — le
  guide pratique opérationnel.
- [Honnêteté & limites](/fr/start/honesty-and-limits/) — l'engagement permanent sous
  lequel cette page est écrite.
