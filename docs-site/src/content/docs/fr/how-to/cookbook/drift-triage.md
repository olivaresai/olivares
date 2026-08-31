---
title: "Recette : trier la dérive de moindre privilège"
description: >-
  Travailler un résultat Permitted-vs-Observed jusqu'à zéro : classer les accès
  inattendus, les habilitations inutilisées et les arêtes en attente de
  réconciliation, décider pour chacune (accorder, révoquer ou corriger
  l'identité), puis re-vérifier — sans se fier au moindre indice.
sidebar:
  order: 4
---

**Objectif :** transformer le résultat de dérive — l'écart entre ce que les
agents *peuvent* faire et ce qu'on *observe* qu'ils font — en décisions, à un
rythme régulier, jusqu'à ce que le diff soit silencieux.

## 1. Récupérer la dérive

```bash
curl -ks "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

(Ou en HCL, pour revue dans une PR : la source de données Terraform
`olivares_access_edges` avec `include_drift = true` —
[gérer comme du code](/fr/how-to/manage-as-code/).)

Le résultat comporte trois classes, et ce sont des problèmes différents :

| Classe | Signification | La question à poser |
|---|---|---|
| **Accès inattendu** | observé, mais aucune habilitation ne le couvre | est-ce une habilitation manquante, ou une vraie violation ? |
| **Habilitation inutilisée** | accordée, jamais observée en usage | pourquoi cette permission existe-t-elle ? |
| **Réconciliation en attente** | observé, mais le lien agent↔identité n'est pas résolu | un problème d'identité, pas (encore) de sécurité |

## 2. Trier chaque classe

**Accès inattendu** — lisez les axes d'honnêteté de l'arête avant d'agir :

- `attribution_tier: firm` + `coverage_tier: clean` est le constat de la plus
  haute qualité que vous obtiendrez : une identité spécifique a touché une
  ressource spécifique et l'audit propre du store l'a classé. Décidez : si
  légitime, déclarez l'habilitation (politique ou liaison) pour que la carte
  reflète l'intention ; sinon, révoquez l'accès sous-jacent et traitez-le comme
  un incident.
- Une attribution `approximate` signifie que l'*accès* a eu lieu mais que le
  *qui* est une credential partagée. Ne gaspillez pas une enquête sur « quel
  agent était-ce » — le correctif durable est
  [l'identité par agent](/fr/how-to/connectors/sso-scim-identity/), et d'ici là
  l'arête dit honnêtement ce qu'elle ne peut pas prouver.
- Une arête reposant uniquement sur un indice `mcp_annotation` n'est **pas une
  preuve** — l'indice n'est pas digne de confiance par spécification.
  Corroborez avec une source observée avant de décider quoi que ce soit.

**Les habilitations inutilisées** sont du sur-provisionnement trouvé
gratuitement : chacune est une candidate à la révocation, avec la réserve que
l'absence d'observation n'a de sens que là où la couverture existe — vérifiez
le palier de couverture de la ressource avant de vous réjouir
([couverture par paliers](/fr/how-to/connect-a-source/#couverture-par-niveaux--soyez-réaliste)).

**La réconciliation en attente** est routée vers le backlog d'identité :
câblez ou corrigez la source de roster qui devrait lier cette credential, et
l'arête se résout à la passe suivante.

## 3. Décider, enregistrer, re-vérifier

Prenez la décision là où elle est gouvernée : déclarez les habilitations comme
du code ([Terraform](/fr/how-to/manage-as-code/)) ou via l'API gouvernée, mettez
la direction risquée derrière une
[approbation](/fr/how-to/cookbook/hitl-approvals/), et laissez le ledger
enregistrer qui a décidé quoi. Puis re-récupérez la dérive : les arêtes
réconciliées sortent du diff — seules les vraies lacunes subsistent. Cette
convergence est tout l'enjeu ; l'estate de démonstration la montre en miniature
([quickstart](/fr/start/quickstart/)).

Dans la console, le panneau *Permitted vs observed* de la **carte d'accès** est
cette recette rendue en direct.

## Rythme

Le tri de dérive fonctionne comme une courte boucle hebdomadaire plus un canal
d'alerte pour la classe à fort signal (écritures inattendues firm + clean).
Routez ces constats vers votre astreinte via une
[destination de notification](/fr/how-to/forward-audit-to-splunk/) plutôt que
d'attendre la passe hebdomadaire.
