---
title: "Introspection MCP et gouvernance du registre"
description: >-
  Inventoriez chaque serveur MCP que vos agents peuvent atteindre, traitez ses
  indications autodéclarées comme non fiables par spécification, scannez le
  catalogue à la recherche d'empoisonnement d'outils et de problèmes de posture,
  et réconciliez avec les registres publics et fédérés.
sidebar:
  order: 7
---

La source `mcp` gouverne la **surface de capacités** que voient vos agents :
elle introspecte les serveurs MCP (outils, ressources, prompts), dérive des
*indications* lecture/écriture de leurs annotations, et — en option —
réconcilie ce qui tourne avec le registre MCP public, vos registres fédérés et
le Docker MCP Catalog, en notant la posture au passage.

Une règle ancre tout ce que cette source émet :

:::caution[Les annotations MCP sont non fiables, par spécification]
Les `readOnlyHint` / `destructiveHint` d'un serveur sont des autodéclarations,
et la spécification MCP indique que les clients DOIVENT les traiter comme non
fiables. Chaque arête que produit cette source est une **indication de capacité
déclarée** — `approximate`, ni observée ni permise — qui fournit la surface à
comparer. Elle est corroborée par des sources observées, jamais jugée fiable
seule.
:::

## Déclarer la source

```json
{
  "sources": [{
    "name": "mcp-estate",
    "kind": "mcp",
    "tenant": "<tenant-id>",
    "config": {
      "config_path": "/etc/claude-code/.mcp.json",
      "posture_scan": "true",
      "registry_enabled": "true"
    }
  }]
}
```

Pointez-la vers les serveurs de l'une ou l'autre façon :

| Clé | Signification |
|---|---|
| `servers` | tableau JSON en ligne de spécifications de serveurs MCP à introspecter |
| `config_path` | chemin vers un `.mcp.json` de Claude Code dont les `mcpServers` sont introspectés |
| `timeout` | délai d'introspection par serveur |

## Les couches de gouvernance (chacune optionnelle, chacune assumée)

- **Scan de posture** (`posture_scan`, `true` par défaut) — scanne les
  métadonnées du catalogue introspecté à la recherche d'empoisonnement
  d'outils, d'injection, d'homoglyphes et de portées trop larges, en notant la
  posture par rapport au OWASP MCP Top-10. Uniquement les *métadonnées* du
  catalogue — elle ne sonde ni n'exploite les serveurs.
- **Registre public** (`registry_enabled`, `false` par défaut ; `registry_url`)
  — enrichissement de provenance en lecture seule depuis le registre MCP
  (preview en amont ; le connecteur autovérifie ce qu'il lit).
- **Synchronisation du registre** (`registry_sync` + `owned_namespaces`) —
  énumérer les espaces de noms en DNS inversé que votre organisation possède
  dans le registre public pour détecter les publications retirées ou non gérées
  (l'angle chaîne d'approvisionnement), et dédouaner vos serveurs internes du
  marquage fantôme.
- **Réconciliation interne** (`internal_servers`) — un tableau JSON de serveurs
  internes approuvés (`{name, registry_name, version}`) ; les serveurs en
  exécution sont réconciliés par rapport à lui, avec détection de dérive de
  version. Ce qui tourne mais ne figure pas sur la liste est un constat
  **shadow**.
- **Registres fédérés** (`federated_registries`) — registres d'organisation
  GitHub BYO, Azure API Center et sous-registres privés implémentant
  l'**OpenAPI de registre `/v0.1`** épinglé.
- **Flux de dépréciation** (`deprecation_feed`) — récupérer à chaque passe le
  registre officiel des fonctionnalités dépréciées de MCP pour détecter la
  dérive de règles ; les règles de dépréciation compilées ne dépendent jamais
  de la récupération.
- **Docker MCP Catalog** (`docker_catalog`) — dérive d'épinglage de digest
  d'image plus provenance Docker-built (signé) vs communautaire (non attesté)
  par serveur.
- **Aperçu de la prochaine révision** (`next_revision_preview`) — introspecter
  les serveurs en mode sans état RC MCP 2026-07-28 tout en annonçant encore
  2025-11-25 ; explicitement un bouton d'aperçu.

Les constats atterrissent par couche : notes de posture, lacunes de provenance,
serveurs fantômes, usage de fonctionnalités dépréciées, dérive de registre.

## Ce que vous verrez dans la console

**MCP & skills** est le catalogue de capacités en direct — serveurs, leurs
outils et indications déclarées, skills, et la manière dont chacun est câblé aux
agents :

<img class="light:sl-hidden" src="/console/capabilities-dark.png" alt="La vue MCP & skills : le catalogue de capacités en direct avec serveurs, outils, câblage et configurations managées." />
<img class="dark:sl-hidden" src="/console/capabilities-light.png" alt="La vue MCP & skills : le catalogue de capacités en direct avec serveurs, outils, câblage et configurations managées." />

Les indications apportent la surface *déclarée* à la **Access map** ; le panneau
de dérive est l'endroit où un outil déclaré en lecture seule observé en train
d'écrire cesse d'être un problème d'indication pour devenir un constat.

## Limites assumées

- **L'introspection est un instantané de ce que les serveurs prétendent.** Un
  serveur peut mentir ; c'est la position propre de la spécification et la
  raison pour laquelle chaque arête est marquée comme elle l'est. La
  corroboration vient des sources observées.
- **Un instantané de registre partiel est une erreur, pas un résultat** — le
  connecteur refuse de noter par rapport à une lecture de registre qu'il n'a pas
  pu achever.
- **Le scan de posture lit des métadonnées.** Il n'exécute pas d'outils, ne fuzz
  pas les serveurs et ne détecte pas une implémentation comportant une porte
  dérobée derrière un catalogue propre.

## Voir aussi

- [Connecter Claude Code](/fr/how-to/connect-claude-code/) — où les indications
  MCP rencontrent la télémétrie de session.
- [Module V — MCP, skills et capacités](/fr/reference/modules/v-capabilities/).
- [Construire et livrer un connecteur](/fr/how-to/build-a-connector/) — le récit
  d'admission signée échouant en mode fermé pour les binaires de connecteurs
  eux-mêmes.
