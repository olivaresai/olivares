> Traduction automatique. La version anglaise fait foi.

# ADR-0006 : Bus d'événements in-process par défaut, agnostique au transport pour NATS

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Olivares AI
- **References:** SDK/runtime/event-bus contract; stack design

## Contexte et énoncé du problème

Les connecteurs remontent leurs observations sur un bus d'événements interne ; les modules et les connecteurs de sortie
s'y abonnent par type. Le binaire unique doit fonctionner sans aucun broker de messages, et pourtant un
déploiement multi-hôte a besoin d'un bus distribué.

## Facteurs de décision

- Aucune dépendance à un broker dans le défaut « binaire unique ».
- Un chemin vers un bus distribué qui ne force pas les abonnés à changer.

## Options envisagées

- **Des canaux Go in-process par défaut, derrière une interface `Bus` agnostique au transport**
  qu'une implémentation distribuée (NATS) peut remplacer.
- **Un broker (NATS) dès le départ.**

## Décision retenue

Option retenue : **un bus à canaux Go in-process comme défaut de la v1**, l'interface `Bus`
n'exposant **aucun canal**, de sorte qu'une implémentation **NATS** peut être insérée pour les
déploiements multi-hôtes **sans changer un seul abonné**. La livraison est
asynchrone et au moins une fois (at-least-once) ; les consommateurs dédupliquent sur l'horodatage de la clé naturelle.

> **Amendé par l'ADR-0017 (2026-06-12) :** le « at-least-once » de la phrase précédente
> était erroné en tant que description de la livraison du BUS — l'implémentation et le
> contrat S02 §4 sont au plus une fois (at-most-once) (les erreurs de handler sont journalisées, les événements en file sont abandonnés à la fermeture) ;
> le at-least-once s'applique à la ré-émission au niveau de la source (réexécutions de `Gather`), qui est ce que
> les consommateurs dédupliquent. L'ADR-0017 consigne le backend NATS livré : fan-out
> local in-proc inchangé + pont NoEcho, at-most-once inter-nœuds, pas de JetStream en v1.

### Conséquences

- **Bon :** le binaire unique n'a besoin d'aucun broker ; le chemin distribué est un remplacement direct.
- **Mauvais / compromis :** la sémantique at-least-once reporte la déduplication sur les consommateurs.
- **Neutre :** NATS est optionnel et réservé à la topologie distribuée.

## Pourquoi les alternatives ont été écartées

- **Broker dès le départ** — ajoute une dépendance externe à chaque installation, mettant en échec
  l'objectif binaire unique / air-gap, pour une valeur dont seule la topologie distribuée a besoin.
