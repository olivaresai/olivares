---
title: "Transmetteur SIEM/ITSM"
description: >-
  Achemine le registre d'audit scellé et chaîné par hachage (hash-chained) ainsi
  que les constats de gouvernance vers vos plateformes SIEM et ITSM dans leur
  dialecte natif — OCSF 1.8, CEF, LEEF, syslog ou OTLP — via la plateforme
  d'événements durable, avec un parcours de curseur arbitré par un leader et une
  livraison au moins une fois. Il rend et transmet ; il ne re-dérive jamais
  l'intégrité.
---

Le transmetteur SIEM/ITSM (`modules/siemforward`) prend les preuves que le
moteur scelle déjà et les fait parvenir à la plateforme qu'exploite déjà votre
SOC. Il est **EN PRODUCTION**. Il ne détient aucune preuve nouvelle : il parcourt
le registre d'audit à altération détectable
et le flux de constats de gouvernance, remet en forme chaque enregistrement dans
le dialecte natif de la destination, puis le confie à la
[plateforme d'événements](/fr/reference/modules/eventing/) pour une livraison
durable. Les champs d'intégrité voyagent à l'identique — jamais re-dérivés en
transit.

## Ce qu'il transmet, et comment

Deux moitiés coopèrent. Un **`SinkRenderer`** (il implémente `eventing.SinkRenderer`)
remet en forme un événement capturé dans le format filaire de la plateforme :

- `audit.recorded` — un enregistrement de registre scellé, rendu via `core/audit`.
- `finding.reported` — un constat de gouvernance (données minimales : hachage plus
  extrait expurgé).
- tout le reste sur le bus — une enveloppe au format neutre qu'un collecteur
  générique peut analyser lui-même.

Dialectes pris en charge : **OCSF 1.8**, **CEF**, **LEEF**, **syslog**, **OTLP**,
et un passthrough JSON structuré. Le renderer est **fermé par défaut**
(deny-closed) : un type de destination inconnu ou un format non rendu renvoie une
erreur, et le moteur réessaie puis place la livraison en file morte (dead-letter)
— jamais un envoi non authentifié ou mal formé.

Une **pompe de transmission arbitrée par un leader** pilote le reste. Chaque passe
lit un curseur par locataire, parcourt le registre depuis la séquence suivante par
lots bornés, et met en file chaque enregistrement. Le curseur n'avance qu'au-delà
des enregistrements mis en file avec succès, de sorte qu'un plantage ou un
redémarrage reprend là où il s'est arrêté — **au moins une fois** depuis le
registre, la source faisant autorité. Les enregistrements re-parcourus sont
dédupliqués en aval.

## Destinations

L'endroit où va le registre est un **abonnement de destination** (sink
subscription) d'événements par locataire, et non une API en libre-service sur ce
module — il ne monte aucune route. Les destinations sont
**provisionnées par l'opérateur** : Splunk HEC, Microsoft Sentinel (Logs
Ingestion / DCR), Datadog Logs, New Relic, ou un collecteur HTTPS générique. Le
moteur ouvre l'identifiant scellé et détient le transport ; le renderer ne
conserve ni état ni identifiants, de sorte qu'une seule instance dessert chaque
locataire et chaque destination.

## Contexte délimité, énoncé clairement

- Il **transmet**, il ne stocke pas. Un locataire sans abonnement de destination
  est une opération nulle : rien n'est mis en file, le curseur avance tout de
  même, rien n'est perdu.
- La transmission s'exécute depuis le parcours de curseur, **en dehors de la
  transaction de scellement du registre** — une écriture réseau ne se trouve
  jamais sur le chemin de scellement.
- Il s'agit d'un **push vers votre plateforme**, distinct du pull en lecture seule
  de l'[export de posture](/fr/reference/modules/posture-export/). L'ingestion côté
  plateforme est hors périmètre ; nous rendons vers le dialecte publié et nous
  livrons.

## Voir aussi

- [Eventing](/fr/reference/modules/eventing/) — la surface d'abonnement durable
  (réessai/backoff, DLQ, rejeu de curseur) vers laquelle ce module rend.
- [Conformité](/fr/reference/modules/xiii-compliance/) — le paquet de preuves scellé
  et dérivé du registre que ce flux complète.
- [Transmettre l'audit vers Splunk](/fr/how-to/forward-audit-to-splunk/) — le chemin
  par lecture de fichier (file-tail) quand vous ne pouvez pas provisionner une
  destination native.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — ce que « au moins une fois »
  et « provisionné par l'opérateur » signifient pour cette surface.
