> Traduction automatique. La version anglaise fait foi.

# ADR-0010 : La licence est une simple attestation — ne jamais conditionner les fonctionnalités

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** licensing design (final decision); API/authz/audit contract (§7, §13.5)

## Contexte et énoncé du problème

Un produit commercial open-core doit décider de ce que son contrôle de licence *fait* à
l'exécution. La tentation est de conditionner des fonctionnalités à une vérification de
licence. Pour un produit de sécurité qui constitue aussi une surface potentielle de
contournement d'autorisation, cela entre en conflit avec une philosophie de « pur double
licence, rien de bridé ».

## Critères de décision

- Ne pas amputer le produit ouvert.
- Ne pas faire de la validation de licence une surface de contournement de l'autorisation.
- Fonctionner en environnement air-gapped (isolé du réseau), sans serveur de licences.

## Options envisagées

- **Validation de licence par simple attestation**, qui ne bloque jamais rien.
- **Conditionnement des fonctionnalités** (feature-gating) selon le niveau de licence.

## Décision retenue

Option choisie : **simple attestation**. La validation de licence enregistre le titulaire
et le statut, à titre informatif ; elle ne **désactive, ne dégrade ni ne bloque** jamais
aucune requête, aucun module ni aucun démarrage **dans le binaire ouvert (AGPL)**. La
validation est **hors ligne** (une signature Ed25519 ; pas de serveur de licences). Le
binaire ouvert est la plateforme de gouvernance CORE complète ; l'édition commerciale
ajoute les add-ons `enterprise/` distincts et additifs (il s'agit d'open core, et non « du
même produit complet » — voir l'amendement ci-dessous et ADR-0011).

### Conséquences

- **Bon :** le produit ouvert n'est jamais amputé ; les vérifications de licence ne peuvent
  pas être transformées en contournement d'autorisation ; le produit fonctionne en
  air-gapped.
- **Mauvais / compromis :** la différenciation commerciale provient des *termes de la
  licence* et des modules `enterprise/` distincts, et non d'un conditionnement du noyau.
- **Neutre :** les tests de licence sont fail-open (échec ouvert) par conception.

## Pourquoi les alternatives ont été rejetées

- **Conditionnement des fonctionnalités** — rend l'édition ouverte moins bonne, érode la
  confiance et transforme une licence falsifiable en un verrou relevant de la sécurité.
  Rejeté.

## Amendement (2026-06-23)

Cet ADR demeure valable, avec un périmètre précis : **la clé de licence ne conditionne
jamais le binaire OUVERT.** Le modèle est **open core** (amendement d'ADR-0011) ; la
formulation antérieure « le produit gratuit et le produit sous licence sont le même
produit complet » était donc inexacte et est corrigée ci-dessus — l'édition commerciale
comprend les add-ons `enterprise/` additifs (qui sont les « modules `enterprise/`
distincts » que cet ADR a toujours cités). Une revendication attestée n'est *consommée*
que par la compilation enterprise fermée, et uniquement pour donner droit à ces add-ons
additifs — une décision locale dans la propre compilation commerciale du client. Le cloud
propre d'Olivares ne doit toujours jamais faire confiance au statut autodéclaré pour la
facturation.

**Amendement (2026-07-27, B10).** La seule consommation liée aux sièges que cette note
décrivait auparavant — `enterprise/seats` lisant `license.Claims.MaxUsers` pour lever un
plafond de sièges utilisateur community — a disparu : les comptes utilisateur auto-hébergés
sont ILLIMITÉS dans chaque édition, le plafond de 3 a été supprimé et `MaxUsers` sert
uniquement à l'affichage partout. Cela renforce cet ADR au lieu de le nuancer : aucune
compilation, ouverte ou commerciale, ne lit désormais une licence pour plafonner les
utilisateurs. Voir `LICENSING.md` et
le canon commercial des prix (maintenu en privé).
