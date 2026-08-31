> Traduction automatique. La version anglaise fait foi.

# ADR-0011 : Frontière de licence — produit AGPL, SDK/connecteurs Apache, enterprise commercial

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** licensing design (final decision); stack license boundary

## Contexte et énoncé du problème

Le produit avait besoin d'un modèle de licence qui le garde véritablement ouvert, qui
maintienne un écosystème de connecteurs tiers exempt des frictions du copyleft, et qui
laisse une voie commerciale propre — sans conditionner les fonctionnalités (voir ADR-0010).

## Critères de décision

- Un produit véritablement ouvert et copyleft (ni source-available, ni amputé).
- Un écosystème de connecteurs permissif pour que des tiers l'étendent librement.
- Une exception commerciale propre pour ceux qui en ont besoin.

## Options envisagées

- **Pur double licence :** produit AGPL + SDK/connecteurs Apache-2.0 + exception commerciale.
- **Open core à fonctionnalités conditionnées** (noyau MIT/Apache + fonctionnalités payantes).
- **Tout en permissif** (noyau MIT/Apache).
- **Source-available** (BSL, SSPL, PolyForm).

## Décision retenue

Option choisie : un **pur double licence**. `core/`, `modules/`, `web/` sont en
**AGPL-3.0-only** ; `sdk/` et `connectors/` sont en **Apache-2.0** ; `enterprise/` est
**commercial** (`LicenseRef-Olivares-Commercial`). La frontière est appliquée dès le
premier commit par des en-têtes SPDX par fichier et une vérification CI : un connecteur
Apache-2.0 n'importe **jamais** le moteur AGPL.

### Conséquences

- **Bon :** le produit est véritablement ouvert et copyleft ; les connecteurs restent
  permissifs et sans friction ; la frontière est appliquée de façon mécanique ; une voie
  commerciale existe sans rien brider.
- **Mauvais / compromis :** les contributeurs doivent maintenir des en-têtes SPDX corrects
  et respecter la frontière d'import (la CI détecte les infractions).
- **Neutre :** l'exception commerciale est en libre-service, complétée par un contact
  enterprise.

## Pourquoi les alternatives ont été rejetées

- **Open core à fonctionnalités conditionnées** — bride le produit (voir ADR-0010), rejeté.
- **Tout en permissif** — donne le noyau sans aucune assise commerciale.
- **Source-available (BSL/SSPL/PolyForm)** — ce n'est pas de l'OSS ; cela tue l'adoption
  dont dépend l'écosystème de connecteurs.

## Amendement (2026-06-23) — le modèle est open core

La **frontière de licence ci-dessus est inchangée et correcte** : `core/`+`modules/`+
`web/` sont en AGPL-3.0-only, `sdk/`+`connectors/` sont en Apache-2.0, `enterprise/` est
commercial et un connecteur Apache n'importe jamais le moteur AGPL. Ce qui est corrigé
est le *cadrage* : le produit livré est **open core** (le modèle GitLab `ee/`), **et non**
un « pur double licence » sans différence de fonctionnalités. La compilation AGPL est la
plateforme de gouvernance complète et n'est jamais amputée de l'intérieur pour pousser à
l'achat — mais elle n'est **pas identique** à l'édition commerciale : la ligne
`enterprise/` (fédération multi-IdP, pare-feu de contenu/DLP, durcissement des hooks,
feed de renseignement sur les menaces, egress des outils serveur, CyberArk Conjur,
boucle fermée des incidents) est du **nouveau code additif qui n'a jamais fait partie de
la compilation ouverte** (pas de rug-pull). Ainsi, « envisagée/retenue : pur double
licence » doit être lu comme la décision sur la *frontière* AGPL/Apache ; la décision
sur les *éditions* ouverte ou commerciale est l'open core. Voir
`LICENSING.md`

La **frontière** de licence de cet ADR n'est pas remplacée. Ce qui a changé séparément est
la **distribution** de la ligne commerciale : le code source d'`enterprise/` n'est plus
livré dans le dépôt public ; il a été déplacé dans un dépôt privé pour que le gating par
build tag soit réel et non cosmétique. C'est une décision de distribution, consignée dans
**ADR-0020** ; la frontière et la licence d'attestation uniquement (ADR-0010) restent
inchangées.

## Amendement (2026-07-28) — deux affirmations devenues caduques dans la note du 2026-06-23

La frontière et le cadrage open-core ci-dessus demeurent. Deux éléments de la liste
enterprise de l'amendement du 2026-06-23 ne décrivent plus le produit ; la note elle-même
est laissée exactement telle qu'elle fut écrite, car elle consigne ce qui était alors cru.

1. **« le droit aux sièges qui lève le plafond d'utilisateurs community » n'existe plus.**
   La décision B10 (2026-07-27) a supprimé entièrement le plafond d'utilisateurs : les
   comptes auto-hébergés sont illimités dans chaque édition,
   `core/auth.CommunitySeatLimit` vaut `0`, `enforceSeatCapTx` est un no-op inconditionnel,
   et aucune compilation — ouverte ou commerciale — ne lit une licence pour plafonner les
   utilisateurs. Décision actuelle : le canon commercial des prix (maintenu en privé)
   (`self_hosted.users: unlimited`) et `LICENSING.md`
2. **« feed de renseignement sur les menaces » ne décrit pas comment l'add-on peut être
   vendu.** `enterprise/threatintel` livre un catalogue de base compilé dans la build,
   ainsi que des artefacts de feed optionnels, signés et versionnés, pour lesquels
   l'opérateur épingle une clé d'éditeur et qu'il applique ; Olivares n'opère aucune
   distribution de feed organisée et ne publie aucune cadence de sortie. Le canon
   commercial (le canon commercial des prix (maintenu en privé), `self_hosted.business.preset`) interdit de le
   commercialiser comme un « feed » tant qu'un feed signé n'est pas réellement exploité.
   La CLI de l'opérateur conserve le terme pour l'artefact qu'elle vérifie et applique
   (`olivares threatintel verify|apply|pull`) : c'est le nom de l'artefact, non une
   affirmation sur qui le publie.

Ces deux éléments ne touchent pas la frontière de licence que cet ADR décide.
