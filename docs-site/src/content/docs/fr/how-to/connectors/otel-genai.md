---
title: "OpenTelemetry GenAI (tout agent instrumenté)"
description: >-
  Alimentez l'access map et le FinOps depuis N'IMPORTE QUEL agent instrumenté
  OTel — LangChain, LangGraph, CrewAI, AutoGen, Google ADK et leurs pairs — via
  le profil d'ingestion gen_ai.* neutre vis-à-vis des éditeurs : opt-in, épinglé
  à semconv v1.41.1, normalisant les trois dialectes GenAI qui coexistent dans
  les flottes réelles.
sidebar:
  order: 4
---

Claude Code est la source coopérative canonique, mais ce n'est pas le seul
agent coopératif que vous exécutez. Le même connecteur qui reçoit la
télémétrie de Claude Code (`kind: claude`) porte un **profil OpenTelemetry
GenAI opt-in, neutre vis-à-vis des éditeurs** : pointez n'importe quel agent
ou framework instrumenté OTel vers le même récepteur OTLP, et sa télémétrie
`gen_ai.*` alimente l'access map et le pipeline de coûts — LangChain,
LangGraph, CrewAI, AutoGen, Google ADK et tout autre composant émettant les
conventions sémantiques GenAI sur des spans ou des événements de log.

## Pourquoi c'est opt-in

Les conventions OpenTelemetry GenAI sont en **statut Development** (pré-stable)
en amont, et trois dialectes coexistent réellement dans les flottes de 2026. Le
profil est donc désactivé par défaut et conditionné exactement comme les SDK
OTel le conditionnent — par le jeton d'opt-in :

```json
{
  "sources": [{
    "name": "agents-otel",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "semconv_opt_in": "gen_ai_latest_experimental"
    }
  }]
}
```

`semconv_opt_in` reflète `OTEL_SEMCONV_STABILITY_OPT_IN` : une liste séparée
par des virgules qui doit contenir `gen_ai_latest_experimental`. Avec le profil
**désactivé**, un enregistrement `gen_ai.*` alimente toujours le watchdog de
vivacité de session mais n'est **pas mappé** — une absence honnête, pas une
ingestion silencieuse.

## Ce que le normaliseur accepte

Le profil est épinglé à **semconv v1.41.1** et normalise les trois dialectes
GenAI qui coexistent dans les estates réels, estampillant chaque événement
normalisé avec l'épinglage semconv du dialecte afin que la provenance survive :

| Dialecte | Forme |
|---|---|
| OpenLLMetry historique | attributs indexés `gen_ai.prompt.{i}.*` |
| v1.36 et antérieures | les événements par message désormais dépréciés |
| v1.37+ | la génération `messages` |

Au-delà des formes de message, il mappe les **conventions `mcp.*` (v1.39)** et
la **séparation client/internal d'`invoke_agent` plus `invoke_workflow`
(v1.41)** — de sorte que les invocations d'agents et de workflows orchestrées
par un framework atterrissent comme une topologie structurée, et non comme du
bruit. L'émission basée sur les spans (la manière dont LangGraph, LangChain,
CrewAI, AutoGen et Google ADK instrumentent) et l'émission basée sur les logs
sont toutes deux ingérées.

Les échantillons de coût sont dédupliqués par identifiant de span W3C, de sorte
qu'un agent dont la télémétrie arrive à la fois par le chemin span et par le
chemin log n'est jamais facturé en double.

## Câbler un agent

Le récepteur est le point de terminaison OTLP propre au connecteur (gRPC
`127.0.0.1:4317`, HTTP `127.0.0.1:4318` par défaut). Côté agent, la
configuration standard du SDK OTel s'applique — point de terminaison de
l'exporteur vers le récepteur en loopback, et l'opt-in GenAI si votre
instrumentation y est conditionnée :

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
```

:::caution[La même règle de loopback que Claude Code]
L'ingestion coopérative est **non authentifiée** et se lie au loopback par
défaut. Tout ce qui peut atteindre le socket peut forger de la télémétrie —
gardez-la sur le loopback (`allow_public_bind` existe et est délibérément
marqué DANGEROUS). Les agents hors hôte relèvent du backstop noyau, pas d'un
port OTLP public.
:::

## Ce que vous verrez dans la console

Les sessions instrumentées apparaissent dans **Sessions** comme activité en
direct, attribuées à l'agent émetteur ; leurs appels de modèle alimentent
**Cost & FinOps** ; les spans MCP et d'outils contribuent des arêtes à
l'**Access map** comme toute source coopérative :

<img class="light:sl-hidden" src="/console/sessions-dark.png" alt="La vue Sessions montrant l'activité de session d'agent en direct issue de la télémétrie coopérative." />
<img class="dark:sl-hidden" src="/console/sessions-light.png" alt="La vue Sessions montrant l'activité de session d'agent en direct issue de la télémétrie coopérative." />

## Limites honnêtes

- **Conventions pré-stables, ingestion épinglée.** Le profil est épinglé à
  v1.41.1 ; lorsque l'amont évolue, l'épinglage évolue par une mise à jour
  délibérée, et non par une dérive silencieuse. Une instrumentation qui émet un
  quatrième dialecte n'est pas devinée.
- **Coopératif veut dire coopératif.** Un agent qui n'émet pas est invisible
  pour ce chemin — c'est à cela que servent [eBPF/Tetragon](/fr/how-to/connectors/ebpf-tetragon/)
  et l'audit natif au store.
- **Les particularités de span-kind des frameworks sont réelles.** Certains
  frameworks émettent des spans dont le kind ne correspond pas aux règles
  client/internal de v1.41 ; le normaliseur mappe ce qu'il peut prouver et
  laisse le reste non mappé plutôt que de mal l'attribuer.

## Voir aussi

- [Connecter Claude Code](/fr/how-to/connect-claude-code/) — le même récepteur,
  surface spécifique à Claude.
- [OTel d'entreprise pour Claude Code](/fr/how-to/claude-code-enterprise-otel/) —
  posture de télémétrie à l'échelle de la flotte.
- [Référence des événements](/fr/reference/events/) — les observations normalisées
  que cela produit.
