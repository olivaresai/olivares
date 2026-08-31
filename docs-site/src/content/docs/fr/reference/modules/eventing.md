---
title: "Plateforme d'événements et de webhooks"
description: >-
  La surface d'abonnement destinée aux intégrateurs, posée sur le bus
  d'événements du moteur : abonnements typés à des événements avec livraison
  de webhooks signés, sémantique durable « au moins une fois », nouvelle
  tentative/backoff, une file de lettres mortes et la relecture par curseur.
  C'est la frontière de durabilité que le bus en cours de processus ne fournit pas.
---

Eventing (`modules/eventing`, **LIVE**) transforme le bus d'événements
en cours de processus du moteur en une **surface d'abonnement externe**. Là où le
bus lui-même est « au plus une fois » et abandonne les événements à l'arrêt, ce
module est la **frontière de durabilité** : une fois qu'un événement est capturé
dans la transaction de capture, la livraison est durable et auditable. Ses routes
sont montées sous `/v1/m/eventing/`.

## À quoi vous vous abonnez

Un **abonnement** enregistre les types d'événements souhaités, un filtre de source
optionnel, l'URL d'un point de terminaison consommateur, le rôle sous lequel ses
livraisons sont autorisées, et un secret de signature HMAC généré par le serveur
(renvoyé exactement une fois, puis conservé uniquement à travers la jointure scellée
au repos). Les types abonnables proviennent d'un catalogue typé — `GET /event-types`
renvoie chaque type avec son palier de stabilité et la permission qui le contrôle.
La gestion des abonnements est privilégiée et auditée : create/update/rotate-secret
relèvent du palier write ; delete, replay, redeliver et les livraisons de test relèvent
du palier admin.

## Garanties de livraison

La livraison est **« au moins une fois » avec des clés d'idempotence côté consommateur**
— « exactement une fois » a été rejeté comme une fausse promesse. Chaque événement
capturé devient une ligne de livraison durable par abonnement correspondant, mise en
file d'attente dans la même transaction. Les workers réclament les lignes par version
optimiste (sûr en HA), envoient en POST l'enveloppe d'événement signée, et soit
acquittent (2xx) soit planifient la tentative suivante :

- **Nouvelle tentative/backoff** — 408/425/429/5xx et les erreurs réseau sont
  réessayés selon un calendrier de backoff ; tout autre statut est terminal. Les
  redirections ne sont jamais suivies.
- **File de lettres mortes** — les livraisons épuisées atterrissent dans le statut
  `dead` ; un statut `denied` enregistre un refus RBAC par événement.
- **Relecture par curseur** — une séquence monotone par tenant (allouée depuis une
  ligne de curseur, pas `max(seq)`) vous permet de relire depuis un point du journal
  durable, dans la limite de la fenêtre de rétention.

Chaque tentative porte la signature HMAC-SHA256 horodatée de style Stripe ainsi qu'un
identifiant d'événement stable comme clé d'idempotence. Avant chaque tentative, le
dispatcher exécute l'intégralité du pipeline RBAC+ABAC en mode d'échec fermé contre le rôle
de l'abonnement, de sorte qu'un événement sortant est filtré exactement comme le serait
une lecture en direct.

## Contexte délimité, énoncé clairement

- Le **bus en cours de processus est « au plus une fois »** avec abandon à l'arrêt ;
  la durabilité commence à la transaction de capture, pas à la publication. Les
  événements publiés alors qu'aucun abonnement activé ne correspond ne sont pas
  capturés (économe en stockage), de sorte que la relecture ne remonte que jusqu'à la
  capture.
- Le pont NATS multi-nœuds est honnêtement **« au plus une fois »** — cette plateforme
  est la couche durable au-dessus de lui, et non une garantie portant sur le bus
  distribué lui-même.
- C'est la surface **destinée aux intégrateurs** ;
  [notify](/fr/reference/modules/xv-notify/) reste le routeur d'alertes destiné aux
  opérateurs. Voir
  [honnêteté et limites](/fr/start/honesty-and-limits/) pour les conventions live /
  à la demande / échec fermé.

## En lien

- [Transfert SIEM](/fr/reference/modules/siemforward/) — expédie le journal d'audit scellé
  et les findings vers les tours SIEM ; construit directement sur cette plateforme.
- [Notify](/fr/reference/modules/xv-notify/) — le routeur d'alertes destiné aux opérateurs
  vers des destinations provisionnées.
- [Référence des événements](/fr/reference/events/) — le vocabulaire d'événements auquel
  vous vous abonnez et la forme de l'enveloppe livrée.
