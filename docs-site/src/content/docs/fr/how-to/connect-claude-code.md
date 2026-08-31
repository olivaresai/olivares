---
title: "Connecter Claude Code (le chemin coopératif)"
description: "Pointez l'exporteur OpenTelemetry de Claude Code vers le moteur et câblez-le comme source pour que sa télémétrie d'outil — plus l'introspection MCP non fiable — alimente le R/RW access map."
---

Claude Code est la **source coopérative canonique** d'Olivares AI. Il émet de la
télémétrie OpenTelemetry (OTLP) sur les outils qu'il exécute, et les serveurs MCP
auxquels il parle exposent des indices d'introspection (`readOnlyHint` /
`destructiveHint`) sur le fait qu'un outil lit ou écrit. Ensemble, ils alimentent
le **module III — le R/RW access map** avec des arêtes de haute fidélité,
attribuées à l'agent, la moitié coopérative de l'image permitted-vs-observed.

Cette page câble ce chemin : pointez l'exporteur OTLP de Claude Code vers le
récepteur du moteur, puis déclarez la source pour que sa télémétrie devienne des
arêtes d'accès. Pour le mécanisme général de câblage de source et la place de ceci,
voir [Connecter une source](/fr/how-to/connect-a-source/) et l'
[aperçu de l'architecture](/fr/explanation/architecture/overview/). Pour la forme
des événements normalisés que cela produit, voir la
[référence des événements](/fr/reference/events/).

:::note[Coopératif, pas faisant autorité]
Le chemin coopératif est **de haute fidélité mais à niveaux de confiance**. La
télémétrie d'outil OTLP est attribuée à une session d'agent concrète ; les
annotations MCP sont un *signal* R/RW utile mais sont **non fiables selon la spec
MCP** et sont corroborées, jamais crues seules (voir
[Honnêteté et limites](/fr/start/honesty-and-limits/)). Pour l'activité hors de la
coopération de l'agent — ou pour attraper un agent qui cesse d'émettre — associez
ceci à un filet de sécurité non coopératif (noyau/eBPF) et à l'audit natif du store
(pgAudit, CloudTrail). Cette page ne couvre que la source coopérative.
:::

## Ce que vous obtenez de cette source

Une fois câblée, la télémétrie de Claude Code est normalisée dans le modèle de
données du moteur et fournie au module III :

| Sortie | Provenance | Notes |
|---|---|---|
| **Arête d'accès** `agent session → resource (read/write)` | source de signal `otel` | confiance `attributed` — l'origine est une session concrète, pas un compte de service partagé |
| **Arête de serveur MCP** `session → MCP server` | source de signal `otel` | mode `unknown` (une connexion n'est pas en soi un accès ; c'est de la topologie/inventaire) |
| **Indice R/RW de l'introspection MCP** | source de signal `mcp_annotation` | **non fiable** — un signal de corroboration, jamais une arête à lui seul |
| **Échantillon de coût** (usage modèle par requête) | la télémétrie api-request | alimente FinOps, pas l'access map |
| **Finding** (anti-évasion) | lacunes de télémétrie / outils refusés | une session qui cesse d'émettre tout en restant active est signalée |

Le connecteur est **read-first et à données minimales** : il enregistre la
*relation* (quelle session a touché quelle ressource, lecture ou écriture), jamais
le payload. Une entrée d'outil brute ou une commande shell — qui peut transporter
un secret ou de la PII — est réduite à une référence de ressource expurgée avant de
devenir une observation. Cette posture est le défaut ; retenir tout contenu est un
opt-in explicite, à périmètre de catégorie.

## Comment fonctionne le câblage

Il y a deux moitiés, et elles se rencontrent sur un socket loopback de l'hôte où
Claude Code s'exécute.

1. **Le moteur expose un récepteur OTLP comme ingest du core.** Le connecteur
   coopératif exécute un récepteur OTLP (gRPC et HTTP) pour la propre sortie
   OpenTelemetry de Claude Code, plus un endpoint pour ses hooks d'outils. Il **se
   lie au loopback par défaut** — l'ingest coopératif est non authentifié, donc il
   ne doit pas être joignable hors hôte. Gardez-le sur loopback ; le filet de
   sécurité hors hôte est le collecteur noyau, pas un port OTLP public.
2. **Vous pointez l'exporteur OTLP de Claude Code vers ce récepteur**, et vous
   **déclarez la source** pour que le moteur sache l'exécuter pour votre tenant.

```
  Claude Code (agent host)                 Olivares AI engine
  ┌──────────────────────────┐             ┌─────────────────────────────┐
  │ OTLP exporter            │── loopback ─▶│ cooperative OTLP receiver   │
  │ (OTEL_* env on the CLI)  │   (4317/4318)│ → normalize → access edges  │
  │ MCP servers (R/RW hints) │             │ → module III (R/RW map)     │
  └──────────────────────────┘             └─────────────────────────────┘
```

:::caution[Le récepteur est non authentifié et loopback-only par défaut]
Parce que l'ingest coopératif accepte de la télémétrie sans authentifier
l'expéditeur, quiconque peut atteindre le socket peut forger des arêtes. Le
récepteur se lie par défaut au loopback pour exactement cette raison. Le lier à une
adresse non-loopback est un opt-in dangereux et explicite ; ne l'exposez pas sur un
réseau partagé. Les agents hors hôte devraient être observés avec le filet de
sécurité non coopératif à la place.
:::

## Étape 1 — Pointer Claude Code vers le récepteur

Claude Code est configuré via ses propres variables d'environnement OpenTelemetry.
Sur l'hôte de l'agent, activez son export OTLP et dirigez-le vers le récepteur
loopback du moteur. Le récepteur du moteur suit les ports OpenTelemetry standard
(gRPC et HTTP) ; réglez l'endpoint de l'exporteur de Claude Code sur l'adresse
loopback et le protocole correspondants.

:::note[Les noms exacts des variables OTEL appartiennent à Claude Code, pas à ce produit]
L'exporteur est configuré avec les propres réglages de Claude Code / d'OpenTelemetry
(activer la télémétrie, choisir le protocole OTLP, définir l'endpoint). Ces noms
sont définis par Claude Code et le SDK OTel — consultez la documentation de
télémétrie de Claude Code pour les noms de variables actuels plutôt que de copier
une liste ici. Ce que ce produit possède, c'est le **récepteur** vers lequel ils
pointent et la **déclaration de source** ci-dessous.
:::

Par défaut, le connecteur ne retient que la télémétrie **structurelle** — attributs
de session et d'identité, noms d'outils, mode R/RW, timing — et jamais le texte des
prompts, les corps d'outils ou les corps d'API bruts, même si Claude Code est
configuré pour les émettre. Laissez-le ainsi sauf si vous avez une raison
spécifique et auditée de retenir une catégorie de contenu.

## Étape 2 — Déclarer la source

Les sources réelles (non-démo) sont câblées depuis un unique fichier de
configuration possédé par l'opérateur, nommé par la variable d'environnement
`OLIVARES_SOURCES_CONFIG`, que le moteur lit **avant de démarrer**. Les secrets
vivent par valeur dans ce fichier opérateur, jamais dans le store. Chaque entrée
nomme la source, sa `kind`, le tenant auquel elle appartient, et un bloc `config`
par source :

```json
{
  "sources": [
    {
      "name": "claude",
      "kind": "claude",
      "tenant": "<tenant-ref>",
      "config": {
        "grpc_addr": "127.0.0.1:4317"
      }
    }
  ]
}
```

- **`name`** est votre label pour cette instance de source.
- **`kind`** sélectionne le connecteur coopératif Claude Code.
- **`tenant`** restreint chaque arête qu'il produit à un seul tenant (les lectures
  du module III sont restreintes au tenant et privilégiées).
- **`config`** contient les propres réglages du connecteur — par exemple l'adresse
  loopback à laquelle le récepteur OTLP se lie. Le connecteur lie son récepteur
  lui-même plutôt que d'emprunter celui de l'agent, de sorte que désactiver une
  variable OTEL de Claude Code ne peut pas silencieusement éteindre le collecteur.

:::caution[Confirmez les clés de config du connecteur contre le descripteur livré]
Le connecteur publie son propre schéma de configuration (son descripteur liste
chaque clé, type, défaut et description). Le bloc `config` ci-dessus montre la clé
d'adresse de récepteur représentative ; **n'inventez pas de clés additionnelles**
depuis cette page. Lisez le descripteur que le connecteur rapporte — ou
[la référence de configuration](/fr/reference/configuration/) — pour la liste
versionnée faisant autorité (adresses de récepteur, le chemin du hook, fenêtres de
corrélation/silence, l'allowlist de capture de contenu, et les champs de
gouvernance en opt-in). Une valeur à la fois, vérifiée contre ce que votre build
livre réellement.
:::

Une **source non configurée ou vide avertit honnêtement** plutôt que d'échouer : une
`kind` inconnue, non embarquée, ou qui échoue à se charger est rapportée au
démarrage, jamais silencieusement réduite à un no-op. Après avoir édité le fichier,
redémarrez le moteur pour que la racine de composition le relise.

## Étape 3 — Vérifier que les arêtes arrivent

Avec Claude Code en train d'exporter et la source déclarée, exécutez une session
Claude Code qui touche une ressource (lire un fichier, exécuter une commande,
appeler un outil MCP), puis regardez l'access map. Consulter le graphe d'accès est
une **action privilégiée, restreinte au tenant et auditée** (rôle editor et plus —
jamais le viewer le plus bas), donc utilisez un jeton avec le bon rôle :

- Le graphe d'accès est servi sur la route de module `/v1/m/accessmap/graph`.
- Le résultat permitted-vs-observed — le **drift** de least-privilege — est sur
  `/v1/m/accessmap/drift`.

Ces routes de module sont joignables mais sont délibérément **absentes** du document
OpenAPI servi ; leurs contrats vivent dans les interfaces Go/TS typées du produit.
Pour la procédure de bout en bout d'un moteur neuf à un graphe peuplé, suivez le
[tutoriel Zero to graph](/fr/tutorials/zero-to-graph/).

Vous devriez voir des arêtes dont la source de signal est `otel`, attribuées à la
session Claude Code. Si l'introspection MCP a contribué un indice R/RW, celui-ci
arrive comme un signal `mcp_annotation` distinct qui corrobore — mais n'établit pas
à lui seul — le mode de l'arête.

## Limites honnêtes de ce chemin

- **Les annotations MCP sont non fiables.** `readOnlyHint` / `destructiveHint` sont
  des indices consultatifs qu'un serveur déclare sur lui-même ; la spec MCP dit que
  les clients doivent les traiter comme non fiables. Le produit les fait remonter
  comme un signal de corroboration et montre la confiance honnêtement — il ne met
  jamais une arête à niveau en « read-only » sur un seul indice.
- **L'attribution dépend de l'identité par agent.** Les arêtes sont attribuées à une
  identité de session. Un pool d'agents partageant un compte de service effondre
  l'attribution ; résoudre cela est une affaire de gouvernance (émettre et appliquer
  une identité par agent), pas quelque chose que ce connecteur peut fabriquer.
- **Il est coopératif.** Il voit ce que l'agent rapporte. Un agent qui n'émet
  jamais, ou une activité qui se produit hors du chemin de l'agent, est invisible à
  cette source par construction — ce qui est exactement la raison pour laquelle le
  filet de sécurité noyau non coopératif et l'audit natif du store existent à ses
  côtés.
- **Profondeur en phase de conception.** Une grande partie de la plateforme est
  pré-1.0. Traitez les capacités ici comme le chemin d'ingest coopératif vérifié ;
  là où un module ou un champ en aval n'est pas encore construit, le produit le dit
  plutôt que de sous-entendre une couverture.

## Étapes suivantes

- [Connecter une source](/fr/how-to/connect-a-source/) — le modèle général de
  câblage de source (coopératif et non coopératif).
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — transformez le drift
  observé en décision de least-privilege.
- [Référence des événements](/fr/reference/events/) — les observations normalisées
  que cette source émet.
- [Aperçu de l'architecture](/fr/explanation/architecture/overview/) — où se situe
  le chemin coopératif dans la plateforme.
