---
title: "Recette : politiques deny-closed (Cedar / OPA)"
description: >-
  Câbler le point de décision de politique en mode restriction seule : une
  surcouche forbid Cedar ou une politique OPA permit-by-default, validée et
  testée en dry-run avant publication — des politiques qui ne peuvent que
  retirer de l'accès, jamais l'élargir.
sidebar:
  order: 1
---

**Objectif :** ajouter des restrictions basées sur les attributs par-dessus un
RBAC deny-by-default — par exemple, « personne ne touche aux ressources
étiquetées `secret`, quel que soit ce que dit son rôle ».

L'invariant à garder en tête : le PDP **restreint uniquement**. La décision se
compose comme RBAC ∩ ABAC natif ∩ PDP externe — une politique ne peut jamais
accorder ce que le modèle de rôles refuse
([le modèle](/fr/how-to/govern-and-approve/#la-couture-de-politique-abacpdp-ne-fait-que-restreindre)).

## Cedar (embarqué, primaire)

Sélectionnez le moteur et pointez-le sur votre fichier de politique, puis
redémarrez :

```bash
OLIVARES_PDP_ENGINE=cedar
OLIVARES_PDP_CEDAR_FILE=/etc/olivares/policy.cedar
```

Une politique Cedar est une **surcouche forbid** — le permit de base représente
« le RBAC a déjà décidé », et vos règles `forbid` soustraient :

```cedar
permit(principal, action, resource);

forbid(principal, action, resource)
  when { resource.kind == "credential" && resource.sensitivity == "secret" };
```

Deux faits d'écriture, vérifiés contre l'adaptateur : `resource.kind` et
`resource.sensitivity` sont toujours présents sur l'entrée de décision
(référençables sans condition) ; tout autre attribut doit être protégé par
`has()`, sinon la règle ne peut pas s'apparier. Un `permit` que vous écrivez ne
peut jamais élargir la décision.

## OPA (via HTTP)

```bash
OLIVARES_PDP_ENGINE=opa
OLIVARES_PDP_OPA_URL=http://opa.internal:8181
OLIVARES_PDP_OPA_PATH=/v1/data/olivares/decision
OLIVARES_PDP_OPA_TOKEN=<bearer-reference>     # optional
```

Écrivez le Rego en **permit-by-default** :

```rego
package olivares

default allow := true

allow := false if {
  input.resource.sensitivity == "secret"
  input.action == "read"
}
```

`true` = aucune restriction. `false`, un résultat manquant, ou **toute erreur de
transport ou réponse non-2xx échoue en mode fermé** — la requête est refusée,
jamais silencieusement non gouvernée.

## Valider, dry-run, publier

Le module de gouvernance expose un cycle de vie des politiques afin qu'une
mauvaise politique n'atterrisse jamais à l'aveugle :

```bash
# Compile-check the source:
curl -ks -X POST "$BASE/v1/m/governance/pdp/validate" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d @policy.json

# Pre-flight a decision WITHOUT audit side effects:
curl -ks -X POST "$BASE/v1/m/governance/pdp/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"principal":"…","action":"…","resource":{"kind":"credential","sensitivity":"secret"}}'

# Then publish (policy-admin permission):
curl -ks -X POST "$BASE/v1/m/governance/pdp/publish" …
```

`GET /v1/m/governance/pdp/versions` liste ce qui est déployé ;
`POST /v1/m/governance/pdp/explain` explique une décision.

## Vérifier les propriétés de sécurité

- Redémarrez avec un fichier de politique **invalide** : le moteur ne désactive
  que le PDP externe et le journalise — le RBAC et l'ABAC natif continuent de
  gouverner ; le control plane ne tombe pas.
- Chaque restriction appliquée par le PDP est **auditée** — vérifiez le ledger
  après une requête refusée.

## Notes

- Les politiques sont versionnées et publiées, ce ne sont pas des fichiers
  édités à chaud en production — traitez la publication comme un changement
  revu.
- Pour les actions soumises à approbation (plutôt que refusées), voir les
  [approbations HITL](/fr/how-to/cookbook/hitl-approvals/).
