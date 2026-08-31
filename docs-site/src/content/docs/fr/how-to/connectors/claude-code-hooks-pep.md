---
title: "Hooks et application des règles dans Claude Code (le PEP)"
description: >-
  La moitié gouvernance du connecteur Claude Code : des hooks observés par
  défaut, et un point d'application des politiques (PEP) optionnel qui répond
  aux hooks PreToolUse / PermissionRequest par deny ou ask — chaque contrôle
  étant enregistré comme un constat.
sidebar:
  order: 5
---

[Connecter Claude Code](/fr/how-to/connect-claude-code/) câble la moitié
*observation* — télémétrie OTLP en entrée, arêtes d'accès en sortie. Cette page
est la **moitié gouvernance** : les **hooks** de Claude Code rapportent les
décisions d'outils au connecteur, et un **point d'application des politiques
(PEP)** optionnel transforme ce canal en un contrôle — le connecteur répond à
un hook `PreToolUse` / `PermissionRequest` correspondant avec une
`permissionDecision` de `deny` ou `ask`, et enregistre chaque contrôle comme un
constat.

Le comportement par défaut est délibérément en **observation d'abord** : sans
politique d'application configurée, les hooks sont *observés, jamais filtrés*.
L'application est un choix explicite et nommé, et une politique invalide
**échoue au démarrage** — le connecteur ne tournera pas silencieusement sans
gouvernance.

## Fonctionnement du canal de hooks

Le récepteur OTLP/HTTP du connecteur (boucle locale `127.0.0.1:4318` par
défaut) sert aussi le point d'accès des hooks à `hook_path` (par défaut
**`/hooks`**). Sur la machine du développeur, la configuration des hooks de
Claude Code envoie ses événements de hook à ce point de boucle locale — la
syntaxe exacte des paramètres de hooks relève de la documentation propre à
Claude Code ; ce que ce produit possède, c'est le récepteur et la politique
ci-dessous.

Les événements de hook et la télémétrie OTLP relatifs au même appel d'outil sont
**corrélés** (la `correlation_window`, 5 s par défaut, fait attendre un côté
l'autre), de sorte qu'une action filtrée et sa télémétrie atterrissent comme un
récit cohérent, et non comme deux enregistrements déconnectés. Une session qui
continue à émettre des hooks mais devient muette côté OTLP au-delà du
`silence_threshold` (2 min par défaut) est signalée comme une lacune de
télémétrie — le signal anti-évasion.

## Activer l'application

Ajoutez une politique `enforcement` à la configuration de la source
(`OLIVARES_SOURCES_CONFIG`) :

```json
{
  "sources": [{
    "name": "claude",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "enforcement": "{\"rules\":[{\"tool\":\"Bash\",\"decision\":\"ask\",\"reason\":\"shell needs a human\"},{\"resource_kind\":\"file\",\"mode\":\"write\",\"decision\":\"deny\"}]}"
    }
  }]
}
```

Les règles s'appliquent en fonction du nom de l'outil et/ou du type de ressource
et du mode d'accès ; la décision est `deny` ou `ask` (escalade vers l'humain
présent dans la session). Les hooks `PreToolUse` / `PermissionRequest`
correspondants reçoivent cette décision en retour sous forme de
`permissionDecision` de Claude Code ; tout le reste passe en observation. Chaque
contrôle est enregistré comme un **constat**, de sorte que la trace d'application
est interrogeable, et non un folklore.

:::note[Le coupe-circuit prime sur tout]
Si le parc (ou l'agent concerné) est sous le coup d'un
[arrêt d'urgence](/fr/how-to/cookbook/kill-switch-drill/), `claude.tool.use` est
tué au niveau de la couche de gouvernance indépendamment de cette politique — le
contrôle d'arrêt est vérifié avant toute règle par outil, et il échoue en mode
fermé.
:::

## Posture du parc : managed settings, observés

L'application au niveau du hook est une couche. La couche à l'échelle du parc est
le fichier **managed settings** de Claude Code, que la source `managed-settings`
observe en lecture seule :

```json
{
  "sources": [{
    "name": "fleet-policy",
    "kind": "managed-settings",
    "tenant": "<tenant-id>",
    "config": {
      "config_path": "/etc/claude-code/managed-settings.json",
      "expected_policy": "{…governance-authored intent…}"
    }
  }]
}
```

| Clé | Défaut | Signification |
|---|---|---|
| `config_path` | `/etc/claude-code/managed-settings.json` (Linux) | le fichier managed-settings actif de l'hôte (macOS : `/Library/Application Support/ClaudeCode/…`) |
| `scope` | nom d'hôte de l'OS | portée d'attribution (id d'hôte / nom de distribution) |
| `expected_policy` | — | intention rédigée optionnelle ; lorsqu'elle est définie, le connecteur signale la **dérive** (politique permise vs configuration observée). Vide = observation seule |

Observateurs optionnels apparentés sur la source `claude` : `managed_mcp_path`
(modélise l'ordre d'évaluation de la liste d'autorisation managed-MCP et signale
les entrées d'autorisation par nom seul) et `sandbox_path` (constats de posture
sur les paramètres de verrouillage du sandbox) — tous deux en lecture seule,
tous deux inactifs tant qu'ils ne pointent pas vers un fichier.

## Ce que vous verrez dans la console

**Claude Code governance** est la surface de rédaction et de boucle de vérité :
la politique que vous visez, la configuration que les hôtes portent réellement,
et la dérive entre les deux. Les contrôles et les constats de lacune de
télémétrie atterrissent dans **Security** ; la session elle-même reste visible
dans **Sessions** :

<img class="light:sl-hidden" src="/console/claude-policy-dark.png" alt="La vue Claude Code governance — rédaction de politique et posture du parc au même endroit." />
<img class="dark:sl-hidden" src="/console/claude-policy-light.png" alt="La vue Claude Code governance — rédaction de politique et posture du parc au même endroit." />

## Limites assumées

- **Le PEP filtre ce que les hooks rapportent.** Un hôte dont les hooks ne sont
  pas configurés n'est pas filtré — associez le parc à
  l'[observateur managed-settings](#posture-du-parc--managed-settings-observés)
  pour que l'absence soit visible, et au
  [filet de sécurité noyau](/fr/how-to/connectors/ebpf-tetragon/) pour qu'il ne
  soit pas aveugle.
- **`ask` s'en remet à un humain dans la session** — c'est une friction, pas un
  verrou. `deny` est le verrou.
- **Les sous-processus sont hors de portée ici** (les hooks se déclenchent pour
  les appels d'outils propres à Claude Code) ; voyez la
  [page OTel entreprise](/fr/how-to/claude-code-enterprise-otel/) pour ce que
  l'environnement de télémétrie atteint et n'atteint pas.

## Voir aussi

- [Connecter Claude Code](/fr/how-to/connect-claude-code/) — la moitié observation.
- [OTel entreprise pour Claude Code](/fr/how-to/claude-code-enterprise-otel/) —
  télémétrie du parc, étiquettes, traçage.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — le modèle
  d'autorisation auquel le PEP se branche.
