> Traduction automatique. La version anglaise fait foi.

# ADR-0018: Backend voix temps réel — posture dormante documentée en v1, intégration post-v1

- **Status:** accepted
- **Date :** 2026-06-12
- **Décideurs :** Fran Olivares
- **Références :** `modules/liveingest/voice.go:28`
  (`PublishVoiceTelemetry`), `modules/voice` (module XVI)

## Contexte et énoncé du problème

La sonde de télémétrie vocale est construite de bout en bout et validée : `liveingest.PublishVoiceTelemetry`
publie une `voice.Telemetry` figurant sur une liste d'autorisation sous la forme `voice.telemetry.observed`, et le module XVI
l'intègre aux métadonnées de session via un consommateur strict qui revalide. Rien n'appelle le producteur sur aucun
chemin de production — il n'existe aucun backend voix temps réel dans le build — de sorte que la moitié observation est vide.
C'est une pure jointure (seam). La question : intégrer un backend (par exemple LiveKit) maintenant, ou
déclarer la posture ?

## Résultat de la décision

**La v1 livre la sonde dormante et le DIT.** La posture honnête est déjà appliquée dans le code : le
producteur refuse les échantillons supprimables et ne fabrique rien ; le `Start` de liveingest journalise « voice
telemetry probe wired but dormant — no realtime voice backend in this build emits turn metadata » ;
la moitié observation reste visiblement vide plutôt que faussement pleine (jamais de lacune silencieuse —
et tout autant, jamais de plénitude fabriquée). L'intégration d'un backend temps réel concret (LiveKit ou
équivalent) est une **session post-v1, si et quand la demande existe**.

Le travail de scale-out a rendu la jointure honnête en multi-nœuds au passage : un futur répartiteur alimentant
la sonde sur N'IMPORTE QUEL nœud atteint désormais le module voix du leader via le pont NATS (la racine de composition
enregistre le décodeur de payload `voice.Telemetry`), de sorte que la jointure dormante n'est pas silencieusement devenue
une jointure mono-nœud sous HA.

### Conséquences

- **Avantages :** aucune dépendance spéculative ; la jointure est testée (producteur + consommateur + décodeur
  du pont NATS), de sorte qu'une future intégration relève du câblage, pas de la conception.
- **Inconvénients / compromis :** le panneau d'observation voix reste vide en v1 — documenté dans le contrat
  d'UI comme une jointure déclarée, ce qui est l'état véridique.
- **Neutre :** la décision est conditionnée par la demande, pas par l'architecture.
