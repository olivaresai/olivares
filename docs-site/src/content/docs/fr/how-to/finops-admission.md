---
title: "Lire un refus d'admission FinOps et la réconciliation"
description: >-
  Comment un refus de budget apparaît sur le fil, comment fixer la posture
  si le magasin est injoignable, et comment lire la dérive réserve/commit.
sidebar:
  order: 21
---

Un effet facturable doit **réserver** la dépense estimée avant de s'exécuter.
Le plan de contrôle **valide** ensuite le coût mesuré ou **libère** la retenue.

## À quoi ressemble un refus

Un plafond dur (`action=block`) répond **HTTP 402**. Un plafond souple
(`action=throttle`) répond **HTTP 429**. Le corps nomme l'action, le budget et la marge qui manquait. Ce point
d'entrée exige la permission d'écriture des budgets : il répond à un
administrateur. Le proxy d'inférence, lui, ne répète jamais cette raison —
un plafond refuse avec `budget limit reached`, une limite par siège avec
`spend limit reached`, sans nom de budget ni montant.

```bash
olivares finops admission reserve --data @reserve.json -o json
```

## Magasin injoignable (refus par défaut)

Si le magasin de budgets ne peut pas être lu, l'admission **refuse**. La
raison stable est `budget store unreachable (deny-closed)`. HTTP **503**.
Une ligne d'audit `finops.admission.denied` est aussi écrite.

La posture par défaut est **deny**. Pour échouer ouvert, utilisez
`"unreachable": "allow"`.

## Lire la réconciliation

```bash
olivares finops admission reconciliation -o json
```

`drift` est vrai s'il reste des réserves expirées non soldées.
`reconciliation` LIT seulement et ne change rien ; `reconcile` est le travail :
il balaie ces réserves, exige le droit d'écriture sur les budgets et émet le
constat `finops_reservation_drift`. Traitez-le comme une posture, pas comme une
réécriture silencieuse de la dépense.
