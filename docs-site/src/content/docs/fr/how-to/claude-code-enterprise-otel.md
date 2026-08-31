---
title: "Configurer OpenTelemetry pour Claude Code en entreprise"
description: >-
  La posture de télémétrie d'entreprise recommandée pour une flotte Claude Code :
  l'env de managed-settings qui active l'export OTel sanctionné, les labels
  opérateur via OTEL_RESOURCE_ATTRIBUTES qui deviennent des dimensions FinOps, la
  beta de tracing pour la hiérarchie des sous-agents, et les réglages de
  confidentialité — avec les devoirs qu'ils créent, explicités.
---

L'export OpenTelemetry de Claude Code est le **chemin d'observation sanctionné**
pour une flotte gouvernée : il n'est pas conditionné au plan, il transporte une
télémétrie attribuée à la session, et le niveau des managed settings peut
l'activer pour chaque développeur — sans rien proxyfier. Cette page est la
configuration *entreprise* qui se superpose à
[Connecter Claude Code](/fr/how-to/connect-claude-code/) : ce qu'il faut régler à
l'échelle de la flotte, ce que chaque réglage vous apporte, et quel devoir il
crée. Les noms de clés et les sémantiques ci-dessous ont été vérifiés contre la
documentation propre de Claude Code le 2026-06-10 (client 2.1.17x) ; revérifiez-les
là-bas avant d'en encoder de nouveaux — ils évoluent vite.

:::note[L'env managé ne gouverne que Claude Code]
Le bloc `env` managé configure le **processus Claude Code**. Les variables OTEL_*
ne sont **pas** propagées aux sous-processus (commandes Bash, hooks, serveurs MCP) ;
seul `TRACEPARENT` est hérité par les sous-processus shell tant que le tracing est
actif. Planifiez l'observabilité des sous-processus séparément (le filet de
sécurité noyau/eBPF).
:::

## Ce que vous obtenez

| Réglage | Ce qu'il apporte | Devoir qu'il crée |
|---|---|---|
| `env` de télémétrie managée | Chaque session exporte en OTLP vers votre collecteur — une observation qui survit à la config propre du développeur | Aucun — télémétrie structurelle par défaut |
| `OTEL_RESOURCE_ATTRIBUTES` | Labels définis par l'org (équipe, projet, centre de coûts) sur **chaque point de données de métrique et chaque enregistrement d'événement** ; le control plane les route vers les dimensions de dépense FinOps | Garder les valeurs de labels non sensibles ; le connecteur les liste en allowlist et les nettoie |
| Beta de tracing | Les spans `claude_code.llm_request` / `claude_code.tool` portent `agent_id` / `parent_agent_id` — la **hiérarchie de sous-agents par instance** dans le graphe d'accès | Surface beta : vérifier à la mise à jour |
| `OTEL_LOG_TOOL_DETAILS=1` | `tool_parameters` sur les événements d'outil — y compris **quelle commande a été rejetée** sur une décision d'outil refusée | Les entrées d'outil quittent l'hôte : un devoir de résidence/expurgation que vous devez assumer |
| `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` | `app.entrypoint` (cli / sdk-ts / claude-vscode …) — quelle surface a lancé chaque session | Aucun (label à faible cardinalité) |

## Étape 1 — activer l'export depuis le niveau managé

Rédigez l'`env` de télémétrie dans votre politique de managed settings (le helper
`TelemetryEnv` du connecteur `managed-settings` rend exactement cette posture) :
activez la télémétrie, pointez l'exporteur OTLP vers le collecteur du control plane,
et exportez à la fois les métriques et les logs. Renvoyez la référence complète des
variables à la documentation de monitoring propre de Claude Code — ne recopiez pas
les valeurs à la main d'ici.

:::caution[Ne jamais inliner les identifiants du collecteur]
Un fichier de managed settings est en clair sur chaque hôte. La couche de rédaction
rejette `OTEL_EXPORTER_OTLP_HEADERS` avec une valeur pour exactement cette raison —
authentifiez le collecteur avec mTLS ou une référence de gestionnaire de secrets,
jamais un jeton inliné.
:::

La capture de contenu (prompts, corps d'outils) reste **désactivée** sauf si vous y
optez — et le connecteur du control plane ne retient indépendamment que des données
structurelles, quoi que le client émette.

## Étape 2 — labelliser la flotte pour FinOps

Définissez `OTEL_RESOURCE_ATTRIBUTES` dans le même env managé, en utilisant un
formatage W3C Baggage strict (encoder en pourcentage les valeurs ; ni espaces ni
guillemets) :

```
OTEL_RESOURCE_ATTRIBUTES=team=payments,project=atlas,cost_center=cc-42
```

Depuis le client 2.1.161, ces valeurs accompagnent **chaque point de données de
métrique et chaque enregistrement d'événement**, pas seulement le bloc de ressource
OTLP — et les clés personnalisées ne remplacent jamais les attributs standard. Sur
le control plane, listez les clés que vous honorez dans l'allowlist
`resource_labels` du connecteur claude ; le connecteur nettoie les valeurs et les
attache comme labels sur les arêtes d'identité de la session et sur chaque
échantillon de coût. FinOps promeut `team` et `project` en dimensions de dépense de
premier ordre, de sorte que « découper la dépense Claude Code par équipe »
fonctionne de bout en bout. Les clés absentes de l'allowlist sont écartées —
données minimales par défaut.

## Étape 3 — hiérarchie des sous-agents (beta de tracing)

Activez la beta de télémétrie enrichie plus un exporteur de traces dans l'env managé
pour obtenir des spans. Les attributs d'identité de sous-agent (`agent_id`,
`parent_agent_id`) sont **réservés aux spans** — ils n'apparaissent sur aucune
métrique ni aucun événement de log — et vivent sur les spans
`claude_code.llm_request` (depuis 2.1.139) et `claude_code.tool` (depuis 2.1.145).
Le connecteur les mappe dans le graphe d'accès ainsi :

- `session → identity.subagent` — l'**instance** de sous-agent qui a agi, et
- `parent agent → identity.subagent` — **qui l'a engendrée** (absent pour les agents
  que la session principale a engendrés directement).

C'est ce qui rend distinguables deux sous-agents concurrents du même type — le
`subagent_type` de l'outil `Agent` à lui seul est un label de type, pas une
instance.

## Étape 4 — réglages de fidélité optionnels

- `OTEL_LOG_TOOL_DETAILS=1` ajoute `tool_parameters` aux événements d'outil — sur
  les décisions d'outil refusées aussi (depuis 2.1.157), de sorte qu'un finding de
  rejet peut nommer la commande nettoyée qui a été bloquée. Le connecteur réduit les
  entrées à des références de ressources expurgées à l'ingestion et ne les stocke
  jamais brutes ; mais les valeurs QUITTENT l'hôte du développeur, donc activer cela
  est une décision de résidence délibérée.
- `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` ajoute `app.entrypoint` à toutes les
  métriques et événements (désactivé par défaut). Le connecteur l'enregistre comme
  topologie de session — une flotte embarquée dans un SDK a une posture de risque
  différente d'un usage CLI interactif.

## Limites honnêtes de ce chemin

- **Ingestion loopback non authentifiée.** Le récepteur coopératif se lie au
  loopback par défaut et doit y rester ; tout ce qui est joignable peut forger de la
  télémétrie (voir [Connecter Claude Code](/fr/how-to/connect-claude-code/)).
- **Les sous-processus ne sont pas couverts.** OTEL_* n'atteint pas les
  sous-processus Bash/hook/MCP ; seul `TRACEPARENT` est hérité sous tracing.
- **Le flux de l'admin plane ne voit pas les fournisseurs tiers.** L'API Claude Code
  Analytics ne suit l'usage que sur l'API Claude — Claude Platform on AWS, Microsoft
  Foundry, Amazon Bedrock et Gemini Enterprise Agent Platform (formerly Vertex AI) n'y sont pas inclus. Pour une flotte sur ces
  surfaces, **ce chemin OTel est la seule observation dont vous disposez**, et le
  détecteur de shadow-auth du flux admin ne peut pas les blanchir.
- **Les chiffres de coût ici sont des estimations.** La télémétrie de coût par
  requête est réconciliée avec les rapports de coût faisant autorité ; une seule
  source de coût par session, jamais les deux.

## Étapes suivantes

- [Connecter Claude Code](/fr/how-to/connect-claude-code/) — le câblage de base sur
  lequel cette page s'appuie.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — la moitié application
  (managed settings, hooks, le PEP).
- [Transférer l'audit vers Splunk](/fr/how-to/forward-audit-to-splunk/) — envoyez à
  votre SIEM les findings produits par cette télémétrie.
