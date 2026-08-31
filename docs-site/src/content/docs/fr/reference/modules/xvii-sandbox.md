---
title: "Module XVII — simulation d'agents et sandbox de test"
description: >-
  Exécution isolée et éphémère de scénarios d'agents contre des outils et ressources
  mockés, rejeu déterministe d'une session historique, et comparaison pré/post-déploiement
  de deux variantes — avec une garantie d'isolation honnête et attestée.
---

Le module XVII est la **sandbox de test** : il exécute un scénario d'agent dans un
environnement isolé et éphémère, rejoue une session historique de manière déterministe, et
compare deux variantes avant un déploiement. Il est le frère du module XII (evals) — le XVII
**s'exécute en isolation et produit des sorties**, le XII **mesure leur qualité** — et les deux
sont découplés : aucun n'importe l'autre. Cette page est la référence de ce que fait la sandbox
aujourd'hui et de ses limites honnêtes.

## Ce que c'est

La sandbox catalogue des **scénarios** rédigés par l'opérateur : une séquence d'entrées d'étapes
plus les réponses mockées des outils et ressources qu'une exécution est autorisée à toucher. Un
scénario est une fixture synthétique — aucun secret, aucun handle de production — clampé avant
d'être persisté. Trois flux s'y exécutent :

- **Simulation de scénario** — exécuter les étapes d'un scénario contre ses mocks, produisant des
  sorties par étape (optionnellement notées contre une suite d'evals).
- **Rejeu** — reconstruire la timeline d'entrées d'une session historique et la réexécuter de manière
  déterministe contre des mocks, de sorte que la même entrée produise la même sortie.
- **Comparaison pré/post-déploiement** — exécuter le *même* scénario contre une variante de référence
  et une variante candidate, noter les deux, et enregistrer un verdict (`improved` / `regressed` /
  `unchanged` / `inconclusive`) avec le delta.

## Entités et la garantie d'isolation

Le module possède quatre entités : un **scenario** mutable, un **run** mutable (`running` → terminal),
un **output** par étape en append-only, et une **comparison** pré/post-déploiement en append-only.
Chaque run enregistre *quel* runner l'a exécuté, si ce runner était `isolated`, si l'état éphémère a
été `destroyed`, les compteurs par étape, et — si un scorer était câblé — la suite, le score et le
verdict de pass.

L'isolation est une propriété du fil, attestée par run, pas une affirmation. Le runner in-process par
défaut est **isolé par construction** : il ne reçoit que la spec étape-et-mock et ne détient aucun
handle vers le store, le réseau ou un quelconque secret ; une étape qui demande une ressource absente
des mocks produit un marqueur de mock-miss déterministe et n'atteint jamais une ressource réelle ;
l'état vit dans l'appel et est jeté au retour, donc le run enregistre `destroyed`. Sous provisionnement
de l'opérateur, un **runtime au niveau de l'OS** se tient derrière la même interface — une instance
éphémère, durcie, à egress contrôlé dont le backend (gVisor ou microVM Firecracker) est choisi *par la
politique* et gated par preflight. Chaque run enregistre le backend réel et son flag `isolated`, de
sorte qu'un backend dégradé ou portable est visible et auditable, jamais caché.

## Ce qu'il consomme et produit

La sandbox n'émet pas sur le bus d'événements ; elle produit des **preuves persistées** que d'autres
modules lisent sans s'y coupler. Ses sorties sont notées par le module XII via un adaptateur câblé
uniquement dans la racine de composition — les deux frères partagent un contrat de port mince, pas un
import. Sa comparaison pré/post-déploiement est la **preuve de décision** que le module de déploiement
lit pour gater une promotion, et elle alimente la baseline de régression que le XII suit. Lancer une
exécution, un rejeu ou une comparaison est une action **privilégiée, portée par le tenant, auditée**
(editor et au-dessus pour exécuter ; la comparaison de déploiement est une décision admin).

:::caution[Limites honnêtes]
- **Le runtime par défaut est synthétique uniquement.** Sans runtime au niveau de l'OS provisionné par
  l'opérateur, le runner mock in-process est le backend : il est isolé par construction mais ne s'exécute
  que contre des mocks, il ne peut donc pas atteindre une cible réelle ni adosser une sonde adversariale
  contre une infrastructure en direct (le module XVIII conserve son propre défaut sûr jusqu'à ce que le
  runtime soit provisionné). C'est honnête, pas dégradé — un déploiement par défaut est pleinement
  fonctionnel.
- **Provisionné-mais-incapable échoue en mode fermé.** Lorsqu'une isolation au niveau de l'OS est
  demandée et que l'hôte n'a pas la primitive, le moteur câble la même chose et **chaque run échoue en
  mode fermé** — il ne rétrograde jamais silencieusement vers le runner synthétique ni ne simule une
  microVM. Un run sur un hôte sans isolation est enregistré comme non isolé, jamais comme protégé.
- **Aucun scorer câblé ⇒ « exécuté, non noté ».** Un run portant une référence de suite sans adaptateur
  scorer est enregistré comme exécuté mais non noté — jamais un pass silencieux.
- **Le rejeu est honnête sur les lacunes.** Si la source d'historique ne peut pas reconstruire une
  timeline ordonnée, le rejeu est rapporté dégradé avec zéro étape, jamais fabriqué.
- **Aucune génération de données synthétiques.** C'est un point d'extension post-v1 documenté uniquement ;
  le module ne livre aucun générateur, n'expose aucune route pour cela, et ne produit aucun échantillon.
:::

## Voir aussi

- [Module XII — qualité, evals et test](/fr/reference/modules/xii-evals/) — le frère qui note les sorties.
- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le XVII et la séparation Govern/Actuate.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — la couche Intelligence.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — agir sur un verdict pré/post-déploiement.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — les seams deny-closed à travers le produit.
