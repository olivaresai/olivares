---
title: "Recette : le kill switch de l'estate (et comment l'exercer)"
description: >-
  Un seul appel arrête toute actuation gouvernée de l'estate — ou un seul agent.
  Rapide à enclencher par conception ; la réactivation exige deux humains, et
  l'incident laisse un dossier de preuves. Exercez-le avant d'en avoir besoin.
sidebar:
  order: 5
---

**Objectif :** quand un agent déraille à la vitesse de la machine, l'arrêter —
ou tout arrêter — *maintenant*, avec un seul appel authentifié, puis lever
l'arrêt plus tard sous contrôle double, l'incident entier consigné.

L'asymétrie est le design : **enclencher est rapide** (palier admin, sans
barrière d'approbation — un arrêt d'urgence ne doit jamais attendre dans une
file), **réactiver est lent** (deux humains distincts, et l'incident laisse un
dossier de preuves pour la revue a posteriori). Il n'y a délibérément aucun break-glass autour
de l'arrêt : arrêté *est* l'état sûr.

## Enclencher

```bash
# Stop the whole estate:
curl -ks -X POST "$BASE/v1/m/governance/killswitch" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"scope_kind":"estate","reason":"runaway agent incident #1234"}'

# Or stop one agent (by UUID or external id):
curl -ks -X POST "$BASE/v1/m/governance/killswitch" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"scope_kind":"agent","scope_ref":"agent:billing-reconciler","reason":"…"}'
```

Ce qui s'arrête, immédiatement et en mode fermé : les surfaces d'**actuation**
gouvernées — `claude.tool.use`, `mcp.tool.call`, `deploy.apply`,
`deploy.retire`, `orchestration.schedule.fire`, `voice.session.open`. Les
approbations d'actuation en attente dans le périmètre sont **annulées dans la
même transaction**, de sorte que rien d'approuvé-mais-pas-encore-exécuté ne
passe après l'arrêt.

Ce qui délibérément ne s'arrête *pas* : l'observation, et la gouvernance
elle-même (constats, cycle de vie des identités, conformité) — vous pouvez
encore voir et gouverner pendant l'arrêt. Réenclencher un périmètre déjà arrêté
renvoie `409` (c'est idempotent sur le périmètre, ce n'est pas une pile).

```bash
# Live posture — is anything stopped right now?
curl -ks "$BASE/v1/m/governance/killswitch/state" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Les règles de gardien peuvent enclencher le même arrêt automatiquement (actions
`stop_agent` / `stop_estate`) lorsqu'une règle de confinement se déclenche — le
chemin automatique et le chemin humain sont la même barrière, et un arrêt
automatique émet un constat CRITICAL.

## Réactiver (contrôle double)

```bash
curl -ks -X POST "$BASE/v1/m/governance/killswitch/$STOP_ID/reenable" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"reason":"root cause fixed: …"}'
```

Ceci **ouvre une approbation**, ne lève jamais l'arrêt directement. L'action
est pré-classée CRITICAL : **deux approbateurs humains distincts**,
authentification forte (AAL3) par décision — et le plancher des deux humains
est structurel, imposé dans la transaction même si une politique d'approbation
tente de rétrograder le palier. Le demandeur ne peut pas être décideur ; une
demande rejetée ou expirée ouvre un nouveau quorum.

Après réactivation, une **revue a posteriori** par encore un autre humain
(différent de celui qui a enclenché, du demandeur *et* des réactivateurs) clôt
l'incident — tant qu'elle n'est pas enregistrée, le même périmètre ne peut pas
être de nouveau arrêté-puis-réactivé sans revue :

```bash
curl -ks -X POST "$BASE/v1/m/governance/killswitch/$STOP_ID/review" … 
curl -ks "$BASE/v1/m/governance/killswitch/$STOP_ID/evidence"   # the evidence pack
```

Le endpoint de preuves renvoie le dossier de l'incident — l'arrêt, les
approbations annulées, les décisions et la piste — prêt pour l'auditeur.

## La console

**Kill switch** dans la section Management est la version en un clic de la même
barrière, avec l'état en direct et le flux de réactivation :

<img class="light:sl-hidden" src="/console/killswitch-dark.png" alt="La vue console Kill switch : état de l'estate et historique par arrêt." />
<img class="dark:sl-hidden" src="/console/killswitch-light.png" alt="La vue console Kill switch : état de l'estate et historique par arrêt." />

## L'exercer

Un kill switch que vous n'avez jamais actionné est une hypothèse.
Trimestriellement, dans une fenêtre de maintenance :

1. Enclenchez un arrêt **à portée agent** sur un agent à faible enjeu ;
   vérifiez que ses appels d'outils sont refusés et que le constat se
   déclenche.
2. Parcourez la réactivation : deux approbateurs, revue a posteriori, dossier
   de preuves récupéré et archivé.
3. Chronométrez la boucle de bout en bout — ce nombre est votre latence de
   confinement réelle, et l'exercice laisse une piste de ledger complète pour
   l'attester.
