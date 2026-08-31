---
title: "Plan d'administration Anthropic (usage, coût, conformité)"
description: >-
  Gouvernez l'organisation Claude elle-même : coût facturé et usage faisant
  autorité via l'Admin API, allow-sets MCP et server-tool côté API comme arêtes
  permises, le flux d'activité de conformité et l'annuaire de l'org — chaque
  identifiant à périmètre défini, chaque angle mort nommé.
sidebar:
  order: 6
---

La télémétrie de Claude Code vous dit ce qui s'exécute sur les machines des
développeurs. Le **plan d'administration Anthropic** vous dit ce que fait
l'*organisation* : coût facturé, usage par workspace, membres et clés de l'org, le
flux d'activité de conformité. Quatre sources en lecture seule le couvrent ; cette
page câble les deux centrales et résume leurs compagnes côté roster.

| Source (`kind`) | Ce qu'elle lit | Identifiant |
|---|---|---|
| `claude-api` | usage & coût facturé, inventaire modèle/workspace, analytics Claude Code, gouvernance MCP/server-tool côté API | Clé Admin API (`admin_key`) |
| `claude-compliance` | le flux d'activité de conformité (événements de qualité probante) + l'annuaire de l'org | clé du flux d'activité + une Compliance Access Key **distincte** |
| `claude-console` | roster IAM de l'org (membres, rôles) → findings de posture SSO/SCIM | identifiants console |
| `claude-wif` | identités non humaines (comptes de service `svac_…`, identités fédérées) + leurs arêtes de périmètre **permis** | identifiants d'endpoint WIF |

Toutes sont **en lecture seule et deny-closed** : un identifiant vide signifie que ce
flux est éteint et le produit le dit — jamais un inventaire vide fabriqué.

## `claude-api` : coût, usage et gouvernance côté API

```json
{
  "sources": [{
    "name": "anthropic-org",
    "kind": "claude-api",
    "tenant": "<tenant-id>",
    "config": {
      "admin_key": "<admin-api-key-reference>",
      "cost_report": "true",
      "claude_code": "true"
    }
  }]
}
```

Les clés qui comptent (issues du descripteur livré ; défauts entre parenthèses) :

- **`admin_key`** (secret) — la clé Anthropic Admin API. Vide = catalogue hors
  ligne uniquement.
- **`cost_report`** (`true`) — tire le rapport de coût **facturé** (quotidien,
  faisant autorité) aux côtés de l'estimation d'usage dérivée. Le produit garde les
  deux séparés : les estimations se réconcilient contre les chiffres facturés, une
  seule source de coût par session, jamais les deux.
- **`lookback`** (`24h`) / **`cost_lookback`** (`48h`) /
  **`bucket_width`** (`1d` ; aussi `1h`, `1m`) / **`max_pages`** — fenêtres de tirage
  et bornes de pagination.
- **`claude_code`** (`false`) — tire aussi le flux Claude Code Analytics (coût
  estimé par développeur, par modèle) pour la refacturation.
- **`claude_code_shadow_auth`** (`true`) — avec le flux d'analytics activé, signale
  chaque développeur dont l'usage Claude Code est facturé en `customer_type=api` —
  une clé personnelle/API **hors de l'abonnement de l'org**, c'est-à-dire une
  identité et une dépense chevauchant une clé non gouvernée. Mettez `false` seulement
  si votre org exécute intentionnellement Claude Code sur une facturation API.
- **`gateway`** (`direct`) — la surface de déploiement sur laquelle cette org
  s'exécute (`direct | claude-platform-aws | bedrock-mantle | bedrock-legacy |
  vertex | foundry`). Sur une surface sans l'Admin API (Bedrock/Vertex/Foundry),
  l'ingest de gouvernance **se dégrade honnêtement avec un finding de posture** au
  lieu de prétendre un inventaire vide.
- **`mcp_toolsets`** / **`server_tool_grants`** — allow-sets déclarés par
  l'opérateur pour les agents Claude pilotés par API (quels outils MCP, quels types
  de server-tool Anthropic un agent *peut* utiliser). Chaque entrée autorisée
  devient une **arête permise** dans le module III, croisée contre l'accès observé —
  le même diff permitted-vs-observed que partout ailleurs. L'`agent_ref` doit être
  l'id externe de l'agent tel que découvert à l'exécution, sinon le grant est un
  no-op honnête plutôt qu'une fausse correspondance.

:::caution[Le flux d'analytics a une frontière nommée]
Le flux Claude Code Analytics ne suit l'usage que sur l'**API Claude**. Les flottes
sur Claude Platform on AWS, Bedrock, Gemini Enterprise Agent Platform (formerly Vertex AI) ou Microsoft Foundry n'y **sont
pas** — l'absence de findings là-bas n'est pas la preuve d'une absence. Pour ces
surfaces, le [plan OTel](/fr/how-to/claude-code-enterprise-otel/) est l'observation
dont vous disposez.
:::

## `claude-compliance` : le flux de preuves et l'annuaire

```json
{
  "sources": [{
    "name": "anthropic-compliance",
    "kind": "claude-compliance",
    "tenant": "<tenant-id>",
    "config": {
      "api_key": "<activity-feed-key-reference>",
      "compliance_access_key": "<compliance-access-key-reference>"
    }
  }]
}
```

Deux identifiants **distincts**, délibérément :

- **`api_key`** — une clé Admin API avec `read:compliance_activities` ; tire le flux
  d'activité (événements de qualité probante).
- **`compliance_access_key`** — une clé séparée avec
  `read:compliance_org_data` / `read:compliance_user_data` ; active l'ingest de
  l'**annuaire** de l'org (orgs, utilisateurs, rôles, groupes — y compris le signal
  de provisioning SCIM que l'Admin API ne peut pas voir). Vide = annuaire éteint,
  deny-closed.

Le périmètre de suppression (`delete:compliance_user_data`, utilisé par le chemin de
droit à l'effacement) est provisionné séparément et soumis à un contrôle à double
validation — ce connecteur en lecture ne le détient jamais.

## Ce que vous verrez dans la console

Dépense facturée et estimée, découpée par les dimensions que la télémétrie
transporte (les labels team et project deviennent de premier ordre), dans
**Cost & FinOps** ; membres de l'org, identités non humaines et leurs périmètres dans
**Identity & NHI** ; findings de posture (shadow auth, dégradation de surface,
footguns WIF) dans **Security** :

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="La vue Cost & FinOps : dépense par modèle et dimension, avec budgets et alertes." />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="La vue Cost & FinOps : dépense par modèle et dimension, avec budgets et alertes." />

## Limites honnêtes

- **L'autorité de coût est le rapport facturé.** Les chiffres dérivés de l'usage
  sont des estimations et sont réconciliés, jamais doublement comptés.
- **Le plan d'administration voit les surfaces opérées par Anthropic.** Claude hébergé
  par un tiers (Bedrock/Vertex/Foundry) lui est invisible — nommé explicitement via
  `gateway`, couvert par le plan OTel.
- **Les findings de posture de `claude-console` incluent un angle mort :** la console
  ne peut pas observer si SSO/SCIM est appliqué en amont — le finding le dit plutôt
  que de deviner.

## Connexe

- [OTel d'entreprise pour Claude Code](/fr/how-to/claude-code-enterprise-otel/) — le
  plan par session que ces flux au niveau de l'org complètent.
- [Budgets & guardrails FinOps](/fr/how-to/cookbook/budgets-and-finops-guardrails/)
  — transformez le flux de coût en limites appliquées.
- [Connecteurs & niveaux de couverture](/fr/reference/connectors/) — le catalogue
  complet.
