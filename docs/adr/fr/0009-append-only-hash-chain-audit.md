> Traduction automatique. La version anglaise fait foi.

# ADR-0009 : Registre d'audit en ajout seul (append-only), chaîné par hachage et signé

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Olivares AI
- **References:** API/authz/audit contract (§6, decision §13.4); threat model (ledger)

## Contexte et énoncé du problème

Le registre d'audit (audit ledger) est l'un des actifs les plus sensibles du produit : s'il peut être
altéré silencieusement, le produit ment. Il doit rendre toute falsification détectable et permettre des copies
externes et vérifiables — tout en étant honnête sur ce que l'intégrité on-host peut et ne peut pas
garantir.

## Facteurs de décision

- Évidence de falsification (tamper-evidence) : une réécriture de l'historique doit être détectable.
- Vérifiabilité hors machine (off-box) pour la conformité et la réponse aux incidents.
- Aucun nouveau sous-système de stockage pour les points de contrôle (checkpoints).

## Options envisagées

- **Append-only + chaîne de hachage (hash-chain) + points de contrôle signés en Ed25519**, avec export vers une copie
  externe WORM/SIEM.
- **Une simple table d'audit** avec des contrôles au niveau applicatif.

## Décision retenue

Option retenue : un **registre en ajout seul (append-only), chaîné par hachage** ; un point de contrôle est lui-même un événement
d'audit signé (Ed25519, signature détachée), si bien que réécrire l'historique antérieur à un point de contrôle est
cryptographiquement détectable. Le registre s'exporte vers des formats externes SIEM/WORM (CEF,
LEEF, syslog, OTLP — une requête d'export complète, prête à être envoyée en POST, avec la
projection simple de LogRecord comme token d'export à part, `otlp_log_record` — OCSF),
chaque enregistrement portant les champs de chaîne afin qu'un SIEM puisse
re-vérifier hors ligne ; les PII ne sont jamais exportées.

### Conséquences

- **Bon :** évidence de falsification sans table de point de contrôle séparée ; re-vérification hors ligne ;
  export prêt pour SIEM.
- **Mauvais / compromis :** la clé de signature sur disque n'arrête pas un root de l'hôte / un super-utilisateur
  de la base de données — donc l'**export externe WORM/SIEM est le véritable contrôle anti-falsification**, et
  la documentation le dit.
- **Neutre :** l'export était en mode pull lorsque cette décision a été prise ; une
  jointure (seam) de forwarder push automatique existait mais n'était pas encore
  implémentée.

  > **Amendement de statut, 2026-07-25.** Le forwarder push est implémenté et câblé :
  > `modules/siemforward` satisfait `audit.Forwarder` et `cmd/olivares/boot.go` démarre
  > une pompe de journal par locataire dès qu'un abonnement d'eventing `audit.recorded`
  > existe. `NopForwarder` s'applique lorsqu'aucun forwarding n'est configuré. L'export
  > pull reste disponible sans changement. La décision elle-même est inchangée.

  > **Amendement de statut, 2026-07-28.** Lorsque cette décision a été prise,
  > l'affirmation ci-dessus « un SIEM puisse re-vérifier hors ligne » n'était vraie
  > que pour la LIAISON de la chaîne et la signature d'un checkpoint : les
  > projections ne transportaient pas l'engagement sur les métadonnées d'un
  > événement, si bien que le hash propre d'un enregistrement ne pouvait pas être
  > recalculé à partir d'une ligne exportée. C'est désormais possible : chaque
  > entrée consommée par le hash de chaîne voyage dans chaque dialecte, engagement
  > sur les métadonnées compris, et cet engagement est AVEUGLÉ par enregistrement,
  > de sorte que compléter la préimage ne révèle rien des métadonnées sous-jacentes.
  > Trois affirmations restent distinctes, et la phrase de cette ADR ne couvre que
  > la première : recalcul de la préimage, PAS authenticité (une clé de confiance
  > externe), PAS exhaustivité (des enregistrements adjacents et un checkpoint).
  >
  > Deux conséquences doivent être consignées. Les deux règles de hash des
  > métadonnées restent désormais actives de façon permanente, distinguées par
  > ligne au moyen d'un aveuglement stocké, car un ledger en ajout seul ne peut pas
  > reformuler la règle de hash de lignes déjà scellées sans rendre un historique
  > légitime indiscernable d'un historique forgé. Le format d'archive a aussi reçu
  > une version pour transporter l'aveuglement, l'ancienne version restant acceptée
  > à jamais pour la même raison : on ne peut pas retirer sous un artefact conçu
  > pour être lu des années plus tard la version avec laquelle il a été écrit. La
  > décision elle-même est inchangée.

## Pourquoi les alternatives ont été écartées

- **Simple table d'audit** — ne fournit aucune évidence cryptographique de falsification ; inacceptable pour un
  produit de sécurité dont l'intégrité du registre est « tout ».
