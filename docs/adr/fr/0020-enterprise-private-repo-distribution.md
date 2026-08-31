> Traduction automatique. La version anglaise fait foi.

# ADR-0020: Édition enterprise distribuée depuis un dépôt privé distinct

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** ADR-0010 (license is attestation-only), ADR-0011 (license boundary),
  `LICENSING.md`

## Contexte et énoncé du problème

Le modèle de licence est open core : le noyau/les modules/le web sous AGPL constituent
l'édition community complète, le SDK/les connecteurs sont sous Apache-2.0, et la branche
`enterprise/` contient du code commercial additif compilé uniquement avec
`-tags enterprise` (ADR-0011). Pourtant, jusqu'à présent, le code source d'`enterprise/`
**était livré dans le dépôt public**. Puisque le gate d'activation est le build tag (et non
la licence, qui sert uniquement d'attestation selon l'ADR-0010) et que la licence ne
conditionne jamais le runtime, n'importe qui pouvait exécuter
`git clone && go build -tags enterprise` et obtenir gratuitement le binaire commercial
complet. Le rempart commercial reposait entièrement sur la licence juridique (système
d'honneur appliqué à un code source visible).

## Facteurs de décision

- Rendre le gating par build tag **réel**, et non cosmétique : le binaire commercial ne doit
  pas pouvoir être compilé par n'importe qui à partir du code source public.
- Maintenir le binaire community AGPL **strictement identique bit pour bit** — aucun rug
  pull, aucune fonctionnalité déjà distribuée en open source retirée.
- Préserver sans aucun changement la frontière de licence par répertoire (ADR-0011) et la
  licence d'attestation uniquement (ADR-0010).

## Options envisagées

- **Conserver `enterprise/` dans le dépôt public** (modèle GitLab avec `ee/` dans un dépôt
  unique, source-available). C'est honnête, mais le rempart est un système d'honneur sur un
  code source visible et librement compilable.
- **Déplacer `enterprise/` dans un dépôt privé distinct** (modèle Grafana : code source OSS
  public + binaire enterprise téléchargeable construit à partir d'un code source privé).

## Résultat de la décision

Option choisie : **déplacer `enterprise/` dans un dépôt privé distinct**. Le dépôt public
ne contient plus l'arbre `enterprise/`, le wiring `cmd/olivares` avec
`//go:build enterprise`, ni aucun outil de compilation avec `-tags enterprise`. Le binaire
commercial est construit dans le dépôt privé en superposant l'arbre commercial et le
wiring sur un checkout épinglé de l'arbre public (l'arbre public est un sous-module ; le
wiring est superposé dans le `package main` de `cmd/olivares`, ce que `go.work` ne peut pas
faire par la seule sélection des modules).

Seule la **distribution** change, pas les licences :

- **L'ADR-0011 (frontière de licence) reste inchangée :** `enterprise/` demeure sous
  `LicenseRef-Olivares-Commercial` ; la frontière AGPL/Apache reste intacte.
- **L'ADR-0010 (licence d'attestation uniquement) reste inchangée :** le binaire ouvert ne
  lit toujours jamais une licence pour activer ou désactiver quoi que ce soit. La licence
  ne devient *matériellement* significative que parce que le code source qui lit un claim
  attesté (les droits aux add-ons) n'est plus public — et non parce que la licence a commencé
  à conditionner le runtime.

### Conséquences

- Un `git clone` du dépôt public + `go build -tags enterprise` ne produit plus le binaire
  commercial : le code source nécessaire est privé. Le gating est désormais réel.
- Le binaire AGPL par défaut ne change pas — il n'a jamais lié `enterprise/`.
- Le gate de parité de schéma open≡enterprise (qui a besoin des deux éditions) migre vers le
  dépôt privé, le seul arbre capable de construire les deux.
- Deux dépôts et une petite étape d'assemblage par superposition constituent le coût ;
  l'artefact de la publication publique n'est pas affecté (il était déjà construit avec
  `-tags release`, jamais avec `-tags enterprise`).

## Amendement (2026-07-28) — le droit aux sièges cité ci-dessus a disparu

La décision de distribution demeure : `enterprise/` vit dans un dépôt privé et le gating
par build tag est réel. Ce qui ne tient plus est l'*exemple* utilisé dans les Conséquences
— « le code source qui lit un claim attesté (le droit aux sièges) ». La décision B10
(2026-07-27) a supprimé le plafond d'utilisateurs ; il n'y a donc plus de droit aux sièges
et aucune compilation ne lit une licence pour plafonner les utilisateurs. Les claims
attestés restants ne sont lus que pour donner droit aux add-ons additifs. La phrase
d'origine est laissée telle quelle, car elle consigne ce qui était vrai lors de la prise
de cet ADR. Décision actuelle : le canon commercial des prix (maintenu en privé)
(`self_hosted.users: unlimited`) et `LICENSING.md`
