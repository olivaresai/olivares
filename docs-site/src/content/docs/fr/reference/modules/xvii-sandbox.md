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
- **Génération locale de données.** Générez des entrées reproductibles via l’API du sandbox avec des modèles de texte locaux bornés. Aucun appel à un modèle IA ou au réseau.
:::

## Générer les entrées d’un scénario

La CLI peut écrire les entrées générées directement au format des étapes de
création d’un scénario :

```sh
olivares sandbox generate --count 2 --seed-file seed.txt -o json > steps.json
olivares sandbox scenarios create --name generated --steps-file steps.json
```

`--seed-file -` lit le modèle depuis stdin. Omettez ce flag pour utiliser le modèle
par défaut. Les sauts de ligne du fichier sont conservés ; n’utilisez que du texte
synthétique. `POST /v1/m/sandbox/synthetic-data` exige la même permission que la
création d’un scénario. Il renvoie des `samples`, chacun avec `key` et `input`,
prêts à servir de `steps`. La génération elle-même n’enregistre pas les entrées ;
son événement d’audit contient uniquement leur nombre. Utilisez du texte synthétique,
pas de secrets ni de données de production.

```json
{"subject_kind":"agent","count":2,"seed":"{{subject_kind}}:user{{index}}@example.test"}
```

Les deux entrées sont `agent:user1@example.test` et `agent:user2@example.test`.
Les seules substitutions sont `{{index}}` (à partir de 1) et `{{subject_kind}}` ;
tout autre texte reste littéral. Il s’agit de génération locale de fixtures, pas
de langage généré par un modèle ni de simulation statistique. Les valeurs par
défaut sont le sujet `agent`, le nombre `10` et le modèle
`{{subject_kind}}-sample-{{index}}`. Le nombre est limité à 100, le sujet à 200 octets
et le modèle ainsi que chaque entrée générée à 8192 octets. Les requêtes trop grandes
échouent sans renvoyer d’échantillon partiel. Le lot encodé doit aussi respecter la
limite de requête de 1 MiB de l’API de scénarios, y compris l’échappement JSON et
l’espace des nom, description et sujet bornés. Mocks et formatage supplémentaires
comptent également dans cette limite.

## Exécuter un scénario généré

Activez le module sélectionnable avec `olivares modules on sandbox`. Génération,
création de scénarios et exécution exigent un éditeur ou administrateur ; un viewer
peut inspecter les scénarios, runs et sorties enregistrés.

Créez un modèle sans saut de ligne final :

```sh
printf '%s' '{{subject_kind}}:user{{index}}@example.test' > seed.txt
```

Enregistrez cette réponse synthétique dans `mocks.json` :

```json
[{"resource":"agent:user1@example.test","response":"first synthetic account"}]
```

```sh
olivares sandbox generate --count 2 --seed-file seed.txt -o json > steps.json
olivares sandbox scenarios create --name generated --steps-file steps.json --mocks-file mocks.json -o json
olivares sandbox scenarios run <scenario-id> --variant candidate -o json
olivares sandbox runs get <run-id> -o json
olivares sandbox runs outputs <run-id> -o json
```

Utilisez l’ID de scénario renvoyé par la création, puis l’ID de run renvoyé par
l’exécution. Avec le runner `inproc-mock` par défaut, la première entrée résout la
réponse ci-dessus ; la seconde renvoie `[[mock-miss:agent:user2@example.test]]`.
Un mock manquant incrémente `steps_error`, mais constitue un résultat synthétique
attendu : le run conserve `status: completed`, `steps_total: 2`, `steps_ok: 1`,
`steps_error: 1`, `isolated: true` et `destroyed: true`. Il n’appelle aucune ressource
réelle. Cet exemple ne demande aucun scoring et ne qualifie pas de runtime OS.
La correspondance utilise le texte exact, y compris les sauts de ligne du modèle.

Scénarios, runs et sorties restent disponibles après redémarrage du moteur.
Désactiver le module retire l’accès à ses routes sans supprimer les données ;
le réactiver restaure l’accès. Ces enregistrements sont délimités par tenant :
une autre organisation ne peut pas les lire.

## Voir aussi

- [Module XII — qualité, evals et test](/fr/reference/modules/xii-evals/) — le frère qui note les sorties.
- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le XVII et la séparation Govern/Actuate.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — la couche Intelligence.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — agir sur un verdict pré/post-déploiement.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — les seams deny-closed à travers le produit.
