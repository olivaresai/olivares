---
title: "Module VIII — données, connaissance et contexte"
description: >-
  Le data plane gouverné pour ce que les agents savent et utilisent : bases de
  connaissances et RAG sémantique sur un index vectoriel enfichable, récupération
  gouvernée par identité/classification/résidence, et lineage append-only qui consigne
  les franchissements configurés par l'opérateur tandis que le gate refuse les autres.
---

Le module VIII est le **data plane gouverné** : il construit des bases de connaissances et
exécute un **RAG sémantique** sur un index vectoriel enfichable, gouverne chaque récupération
par identité, classification et résidence, et enregistre un **lineage append-only** de ce qui
a franchi le périmètre et de ce que le gate de résidence a refusé. La résidence est ainsi
étayée par des preuves plutôt que simplement affirmée. Il détient aussi le registre de
prompts versionné, la mémoire d'agent gouvernée et les politiques de contexte/compaction sous
forme de données — pas de promesses.

## Ce que c'est

Le module **orchestre** le data plane ; il ne ré-implémente pas ses voisins. Il extrait du
contenu depuis des **connecteurs de données en lecture seule**, fait passer chaque corps,
modèle de prompt et entrée de mémoire par son **propre mécanisme d'expurgation avant** que quoi que ce soit
ne soit chunké, embeddé, hashé ou stocké, puis gouverne la récupération en regard des grants
que le module d'identité déclare. L'embedding est délégué à une jonction de modèle — le module
n'appelle jamais un fournisseur directement — et le ranking est délégué à une jonction d'index
vectoriel, de sorte que le contrat de gouvernance est identique que la récupération s'exécute
in-process ou en regard d'un backend ANN externe.

La **ligne rouge** est non négociable : le produit gouverne les données du client et ne les
vend ni ne les exfiltre jamais. Les données ne franchissent le périmètre qu'à un point de
passage provisionné par l'opérateur — un fournisseur externe d'embeddings ou une sortie
SIEM/webhook — et le gate de résidence fonctionne en deny-closed pour toute autre destination.
Trois mécanismes l'inscrivent dans la conception — l'expurgation avant indexation, le gate
d'egress et le lineage qui consigne les franchissements effectués.

## Contrat et entités

Le module VIII déclare **huit entités à portée tenant** dans le modèle de données partagé : la
base de connaissances, le document (métadonnées et provenance, jamais le corps), le chunk
(texte expurgé plus une classification et une ACL héritées), le prompt et ses révisions
immuables **append-only**, la mémoire d'agent gouvernée, la politique de contexte/compaction,
et la ligne de lineage **append-only**. Ses routes se montent sous le propre namespace du
module, enveloppées d'authentification, de cloisonnement par tenant et d'autorisation ; lire
la connaissance et le lineage est une action **privilégiée et auditée**.

La récupération est le contrat de sécurité, et **l'ordre est le contrat** : résoudre les grants
de l'identité (fail-closed — une erreur de garde refuse, jamais un allow dégradé), appliquer le
gate de résidence, embedder la requête, puis **filtrer les candidats par classification et ACL
avant le ranking** afin qu'un chunk que l'identité ne peut pas voir n'entre jamais dans
l'ensemble classé, puis classer, puis ajouter la ligne de lineage immuable. Le **gate d'egress**
est composé par-dessus : une base de connaissances verrouillée en résidence refuse l'ingest ou
la récupération avec un embedder qui ferait fuir des données, appliqué à la création, la mise à
jour, l'ingest et la récupération (défense en profondeur). Le contenu des documents voyage par
un contrat de connecteur typé par conception, **pas** par le bus d'événements — les données de
référence en masse ne doivent pas être diffusées.

## Sur le bus d'événements

Le module VIII **produit** des événements [`finding.reported`](/fr/reference/events/) : un
`FindingReport` hashé par ingest lorsqu'un secret ou une PII est expurgé, et un finding lorsqu'un
gate de résidence ou d'egress refuse — détail hashé uniquement, jamais le secret ni le corps.
La forensique et la conformité consomment le lineage et ces findings. Il **ne consomme** rien du
bus pour le contenu : par conception, le contenu emprunte un contrat pull typé, de sorte que le
minimal-data est une propriété du fil, pas un filtre runtime appliqué après coup.

:::caution[Limites honnêtes]
- **La qualité sémantique dépend d'un embedder configuré.** L'embedder par défaut est **local
  et zero-egress** mais **non sémantique** (un repli déterministe par feature-hash). La base de
  connaissances enregistre son modèle d'embedding afin que le repli ne soit jamais pris pour de
  la qualité sémantique, et le binaire avertit une fois lorsqu'il tourne en mode dégradé. Un
  embedder adossé à un modèle est configuré par l'opérateur (`OLIVARES_EMBEDDINGS_*`) ; définissez
  `OLIVARES_EMBEDDINGS_REQUIRE=1` et le boot **refuse de démarrer** plutôt que de servir des
  vecteurs lexicaux comme s'ils étaient sémantiques.
- **La résidence est un gate d'egress fail-closed, pas un réglage d'inférence.** Choisir une
  région d'inférence ne satisfait pas à lui seul une base de connaissances verrouillée en
  résidence — l'embedder doit être prouvablement en région, sinon l'ingest et la récupération
  sont refusés. Une identité sans habilitation ni région se normalise en public / sans région,
  jamais vers un grant plus large.
- **Le ranking par défaut est exact et in-process** (un scan linéaire, adapté à un nœud
  self-hosted ou air-gapped jusqu'à environ 10⁵ chunks par tenant). Un backend ANN externe
  s'enfiche derrière la jonction d'index vectoriel pour le passage à l'échelle ; un backend
  configuré-mais-indisponible **refuse la requête**, ne se rabat jamais en silence sur des
  résultats différents.
- **Le transport en direct des connecteurs est un suivi documenté.** Les connecteurs parsent
  aujourd'hui le format exporté natif avec des fixtures derrière une interface stable ; sans
  export configuré, une source est simplement vide. L'ingest est synchrone ; l'ingest async à
  grande échelle est un suivi.
:::

## Liens connexes

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module VIII et son statut d'actionnement honnête.
- [Référence du bus d'événements](/fr/reference/events/) — l'événement `finding.reported` et sa charge utile.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — le moteur, les jonctions et les couches.
- [Connecter une source](/fr/how-to/connect-a-source/) — enregistrer un connecteur de données en lecture seule.
- [Installation air-gap](/fr/how-to/air-gap-install/) — exécuter le data plane sans aucun egress.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — le contrat d'honnêteté à l'échelle du produit.
