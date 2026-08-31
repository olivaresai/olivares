---
title: "Recette : pousser les constats et le ledger vers votre SIEM"
description: >-
  Créez un sink push — Splunk HEC, Microsoft Sentinel, Datadog ou New Relic,
  ou un webhook générique signé HMAC — et abonnez-le aux constats et au
  ledger d'audit scellé, livrés au moins une fois en OCSF, CEF ou le format
  que parle votre tour de contrôle.
sidebar:
  order: 6
---

**Objectif :** votre SIEM reçoit les constats du control plane *et* son
ledger d'audit à altération détectable en push, sans qu'un forwarder ne suive des fichiers
(tail).

C'est le chemin push S2S (service-to-service) sur la plateforme d'événements. Les
[postures d'export pull et de file-tail](/fr/how-to/forward-audit-to-splunk/) restent
pleinement prises en charge — le pull demeure la bonne forme pour l'archivage WORM
et la re-vérification hors ligne ; le push est la bonne forme pour l'ingestion SIEM
en direct.

## 1. Créer l'abonnement du sink

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "splunk-prod",
    "event_types": ["finding.reported", "audit.recorded"],
    "endpoint": "https://splunk.internal:8088/services/collector",
    "sink_kind": "splunk_hec",
    "sink_format": "ocsf",
    "sink_cred": "<hec-token>"
  }'
```

- **`sink_kind`** sélectionne le dialecte de la tour de contrôle : `splunk_hec`,
  `sentinel_dcr`, `datadog`, `newrelic` — ou omettez-le entièrement pour le
  **webhook générique** (un endpoint HTTPS recevant l'événement JSON, authentifié
  par la signature HMAC du moteur ; rotation avec `…/{id}/rotate-secret`).
- **`sink_format`** : `ocsf` (le défaut pour les sinks SIEM — le schéma orienté IA),
  `cef`, `leef`, `syslog`, `otlp`, `otlp_envelope` ou `json`.

  :::caution[`sink_format` exige un `sink_kind`]
  Un format n'est appliqué que si un type de sink est défini. **Omettre `sink_kind`
  n'est PAS « l'option HTTPS »** : cela sélectionne le webhook générique, qui envoie
  le JSON d'événement Olivares et ne valide jamais `sink_format`. Pour poster un
  dialecte SIEM vers votre propre endpoint, définissez explicitement
  `sink_kind: "https"` :

  ```json
  {
    "event_types": ["audit.recorded"],
    "sink_kind": "https",
    "sink_format": "otlp_envelope",
    "endpoint": "https://collector.internal:4318/v1/logs"
  }
  ```

  Pour `otlp` (et `otlp_envelope`, son alias exact), l'endpoint doit être le
  chemin exact `/v1/logs` du collecteur : le corps est posté tel quel.
  :::
- **`sink_cred`** (le token HEC / bearer DCR / clé API) est accepté une seule fois,
  **scellé au repos, jamais retourné ni journalisé**. Les types fournisseurs l'exigent
  à la création ; le webhook générique n'en a pas besoin.
- **`event_types`** est votre sélection de flux : `finding.reported` pour le rail des
  constats, `audit.recorded` pour le ledger (ci-dessous), ou les deux.

Testez la livraison avant de lui faire confiance :

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions/$ID/test" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

## 2. Le push du ledger, décrit honnêtement

S'abonner à **`audit.recorded`** active la pompe du ledger : le forwarder parcourt
le ledger d'audit scellé de chaque locataire depuis un curseur par locataire et place
chaque enregistrement dans la file du moteur de livraison durable — **au moins une
fois**, dans l'ordre, reprenable. Chaque enregistrement porte ses champs d'intégrité
de chaîne tels quels, de sorte que la copie côté SIEM permet exactement ce que permet
l'export pull : le CHAÎNAGE (`prev_hash` de n+1 égal au `hash` de n)
et une signature de checkpoint sur `hash` sont vérifiables hors ligne, et le `hash`
d'un enregistrement peut désormais être RE-DÉRIVÉ à partir d'UNE seule ligne exportée
— toutes les entrées du hash de chaîne circulent, y compris le texte canonique
`occurred_at` et l'engagement (commitment) de métadonnées. La re-dérivation exacte
octet par octet n'est aujourd'hui démontrée que pour `syslog` et les trois graphies
OTLP, avec les alphabets de valeurs émis par ce ledger (UUID, acteurs `kind:id`, verbes
à points, timestamp de format fixe et condensés hexadécimaux) : syslog remplace CR et
LF par une espace et OTLP remplace l'UTF-8 invalide, de sorte qu'aucune de ces garanties
n'est inconditionnelle ; `ocsf` (la valeur par défaut du sink), `cef` et `leef`
transportent les mêmes champs mais ne sont pas encore reconstructibles octet par
octet, car leur échappement et leur mappage de champs sont avec perte pour les valeurs
de texte libre. Choisissez l'un des tokens démontrés si vous comptez re-dériver. Cet
engagement est aveuglé
par enregistrement : il complète la préimage sans rien divulguer des métadonnées
sous-jacentes. Trois affirmations restent distinctes — re-dériver le hash n'est ni
vérifier l'AUTHENTICITÉ (cela exige une clé de confiance externe) ni la COMPLÉTUDE
(cela exige des enregistrements adjacents et un checkpoint). L'*archive* d'audit reste
l'artefact le plus fort : elle porte les métadonnées elles-mêmes avec leur aléa, et
peut donc aussi répondre QUELLES métadonnées un engagement recouvre.

Trois propriétés à connaître :

- **Pas d'abonnement, pas de travail.** Sans abonné à `audit.recorded`, la pompe
  n'écrit rien — le chemin ne coûte rien tant que vous ne le demandez pas.
- **« Au moins une fois » signifie que des doublons sont possibles** lors d'une
  redélivraison ; dédupliquez sur le numéro de séquence de l'enregistrement par
  locataire.
- **La pompe est gouvernée par le leader** en HA — exactement un nœud forwarde.

## 3. ITSM : les constats sous forme de tickets

Le même mécanisme d'abonnement pilote les destinations ITSM via le rail de
notification — incidents ServiceNow et tickets Jira issus des constats, avec la
sévérité mappée sur la priorité. Configurez-les comme **destinations** de notification
(les connecteurs de sortie `servicenow` / `jira`) plutôt que comme sinks SIEM ; la
[table des destinations de la page Splunk](/fr/how-to/forward-audit-to-splunk/) montre le
schéma.

## Vérifier de bout en bout

1. `…/test` retourne « delivered ».
2. Provoquez quelque chose d'observable (un seuil d'[alerte de budget](/fr/how-to/cookbook/budgets-and-finops-guardrails/),
   un outil refusé) et observez le constat arriver.
3. Pour le ledger : comparez le repère de niveau haut `seq` côté SIEM avec
   `GET /v1/audit/export?from=<seq>` — les flux doivent concorder.

## Notes

- Les endpoints doivent être en **HTTPS** ; le moteur refuse les sinks en clair.
- Les snapshots de posture (récapitulatifs compliance/NHI/constats) ont leur propre
  module d'export sur les mêmes rails — voir le
  [module compliance](/fr/reference/modules/xiii-compliance/).
- La table de décision complète — quand pull, quand tail, quand push — est sur la
  [page de forwarding Splunk](/fr/how-to/forward-audit-to-splunk/).
