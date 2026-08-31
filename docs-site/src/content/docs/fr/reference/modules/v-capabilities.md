---
title: "Module V — MCP, compétences et gestion des capacités"
description: >-
  La couche de gestion des capacités : quel serveur MCP expose quel outil, quelles
  sont son transport et ses références de secrets, quel agent est câblé à quelle
  capacité, son historique de versions, et la santé basique de sa connexion —
  gouverné, audité, et non fiable par défaut là où la spécification MCP l'exige.
---

Le module V est la **couche de gestion des capacités** : il gouverne les outils et
les capacités de vos agents — quel serveur MCP expose quel outil, quels sont son
transport, son périmètre et sa configuration, quelle origine est câblée à quelle
capacité, son historique de versions, et la santé basique de sa connexion. Il se
situe dans la **couche de gestion** et n'a **aucune surface d'actionnement** : il
catalogue, gouverne et audite, mais n'exécute jamais d'outil et ne modifie jamais
un runtime MCP en service.

## Ce que c'est

Le module est une couche construite **au-dessus** de la découverte passive du
module I et de l'introspection des connecteurs. Il ne réimplémente **pas** le
client MCP, et il s'abstient délibérément de re-matérialiser les entités centrales
que l'inventaire détient déjà (les enregistrements de serveur MCP, de compétence,
d'outil et de ressource). À la place, il lit ces entités centrales et ne stocke que
ses **propres** couches, indexées par les références naturelles déjà expurgées des
connecteurs et résolues vers les entités centrales à la lecture — une discipline
de scripteur unique qui l'empêche d'entrer en compétition avec le matérialiseur de
l'inventaire.

Cela est distinct du module III. Le module V répond à *« à quelle capacité un agent
est-il connecté »* ; le [module III](/fr/reference/modules/iii-access-map/) répond à
*« quelle ressource une origine a-t-elle lue ou écrite »*. Ce sont des vues
séparées et le produit ne les confond jamais.

## Son contrat et ses entités

Le module V détient quatre entités de couche (chacune préfixée `capabilities.`) :

| Entité | Ce qu'elle contient |
|---|---|
| **`mcp_config`** | La configuration gérée d'un serveur MCP — transport, périmètre, une **référence** d'endpoint et des **références de secrets**. Il n'existe aucune colonne pouvant contenir un identifiant utilisable. |
| **`config_revision`** | Un instantané en ajout seul (append-only) par version de configuration — l'historique de versions immuable, qui survit à la suppression de la configuration. |
| **`wiring`** | Le graphe de connexion des capacités : une arête `origin → capability` stockée par référence naturelle, jamais par id d'entité centrale. |
| **`health`** | Le dernier signal de connexion observé d'une capacité (`connected` / `degraded` / `down` / `unknown`) — un signal basique, **pas** un SLA. |

Deux propriétés du contrat sont non négociables. **Les annotations d'outils MCP ne
sont pas fiables** : les `readOnlyHint`/`destructiveHint` d'un outil sont un indice
*déclaré* par le serveur, que la spécification MCP impose aux clients de traiter
comme non fiable — chaque projection d'outil porte un drapeau explicite « non
fiable », jamais un badge de sécurité. **Aucune valeur de secret sur le fil** : une
configuration référence les secrets par nom, type et indice masqué ; le backend
rejette les identifiants en ligne dans un endpoint ou une spec plutôt que de les
stocker. Les données minimales sont une propriété du fil, pas une réflexion
après coup.

La lecture du catalogue est contrôlée par RBAC et cadrée par locataire. Modifier
une configuration — et les secrets qu'elle référence — est un **changement
privilégié** consigné dans le registre en ajout seul et chaîné par hachage
(hash-chained), et attribué au principal réel.

## Ce qu'il consomme et produit

Le module V est alimenté par le [bus d'événements](/fr/reference/events/), et non par
son propre sondage. Il réagit à deux canaux :

- **`edge.observed`** — l'usage d'une capacité à l'exécution devient des arêtes
  `wiring`. Le champ `Source` distingue les signaux **observés** (`otel`) des
  signaux **déclarés** (`mcp_annotation`), et un alimenteur de découverte de
  configuration plus récent étiquette les capacités déclarées statiquement avec une
  source `config`.
- **`finding.reported`** — les constats de santé de connexion des connecteurs
  alimentent le statut de dernier signal de la couche `health`.

Il ne produit aucun événement propre et n'envoie rien à l'infrastructure en
service ; sa sortie est lue par l'UI de gestion et par d'autres modules via ses
routes typées.

:::caution[Limites honnêtes]
- **Aucun actionnement.** Le module V gouverne et catalogue ; il n'exécute jamais
  d'outil, ne compose jamais un serveur MCP, et ne modifie jamais un runtime en
  service. C'est une couche de gestion par nature.
- **Plafond de confiance des annotations.** Les `readOnlyHint`/`destructiveHint`
  sont *déclarés* et présentés comme **non fiables** — la corroboration de
  l'intention de lecture/écriture face à des signaux réels est le travail du module
  III, pas de ce module.
- **La santé de connexion n'est pas un SLA.** La couche `health` est uniquement le
  dernier signal de connexion ; le reporting formel de disponibilité, de SLA et de
  tendance relève du module XXII.
- **La découverte est aussi profonde que le sont les connecteurs.** Les capacités
  observées à l'exécution ne se révèlent qu'une fois qu'un agent les sollicite ; les
  surfaces Claude Code déclarées statiquement (sous-agents, Skills, plugins, styles
  de sortie) sont désormais découvertes avant l'exécution par un alimenteur de
  configuration dédié, mais celui-ci n'émet que des **métadonnées structurelles** —
  des noms, jamais des corps de prompts, des contenus de compétences/plugins, ni des
  secrets.
:::

## Voir aussi

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module V et son statut d'actionnement.
- [Module III — carte d'accès et des ressources](/fr/reference/modules/iii-access-map/) — la vue L/LE dont ce module est délibérément distinct.
- [Référence du bus d'événements](/fr/reference/events/) — les charges utiles `edge.observed` et `finding.reported` qu'il consomme.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — la composition moteur-plus-modules.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — agir sur ce que le catalogue révèle.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — le contrat honnête gouverner-vs-actionner du produit.
