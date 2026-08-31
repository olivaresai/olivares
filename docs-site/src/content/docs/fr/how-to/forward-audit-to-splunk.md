---
title: Forwarder vers Splunk (déposez un Universal Forwarder + tail)
description: >-
  Faites entrer les constats de gouvernance du control plane et son ledger d'audit
  à altération détectable dans Splunk en suivant un fichier (tail) avec un Universal Forwarder —
  sans émetteur natif Splunk-to-Splunk. Honnête sur quel flux est lequel.
---

Vous pouvez faire entrer des données Olivares AI dans Splunk **dès aujourd'hui**, sans
attendre un connecteur natif : écrivez les données dans un fichier et pointez un
**Splunk Universal Forwarder (UF)** dessus. L'UF gère le saut Splunk-to-Splunk (S2S)
vers votre indexeur.

:::caution[Il n'y a pas d'émetteur natif Splunk S2S]
Olivares AI **n'implémente pas** le protocole propriétaire de forwarder S2S de Splunk.
Un émetteur S2S natif est post-v1. Les postures prises en charge sont le **forwarding
par file-tail** (un UF suit un fichier qu'Olivares écrit), l'**export pull** (pour
l'archivage WORM et la re-vérification hors ligne), et un **push HTTP via Splunk HEC** —
y compris, depuis le travail d'interopérabilité SIEM, un push du **ledger lui-même**
via un sink d'événements ([pousser vers votre SIEM](/fr/how-to/cookbook/push-to-siem/)).
Cette page documente les chemins fichier et pull ; la recette couvre le push.
:::

Il existe **deux flux différents**, et ce ne sont pas la même chose. Choisissez
délibérément :

| Flux | Ce que c'est | Voies vers Splunk |
|---|---|---|
| **Gouvernance / constats** | le flux de notification que le module IX route (santé, dépense, sécurité, constats de compliance) | le connecteur de sortie `filelog` l'ajoute à un fichier ; ou `splunkhec` le pousse ; ou un [sink d'événements](/fr/how-to/cookbook/push-to-siem/) abonné à `finding.reported` |
| **Ledger d'audit à altération détectable** | la piste d'audit append-only, à chaîne de hachages, signée | l'export **pull** `GET /v1/audit/export` (cette page) ; ou la pompe **push** — un sink d'événements abonné à `audit.recorded`, livré au moins une fois. Il n'y a pas de sink *fichier* natif ; matérialisez un fichier avec l'export planifié ci-dessous |

## Flux A — les constats, via le connecteur `filelog`

Le connecteur de sortie `filelog` ajoute le flux de notification/constats **un
enregistrement par ligne** à un fichier (ou `stdout`/`stderr`), qu'un UF peut suivre.
Configurez une destination de notification de type `filelog` avec ces champs :

| Champ | Signification |
|---|---|
| `path` | cible d'ajout : un chemin de fichier, ou `stdout`/`stderr`/`-` |
| `format` | format par ligne : `json` \| `cef` \| `leef` \| `syslog` \| `otlp` \| `otlp_envelope` \| `ocsf` \| `asim` (défaut `json`) |
| `hostname` | champ syslog `HOSTNAME` (pour le format `syslog`) |
| `fsync` | vider chaque enregistrement sur disque (durabilité pour une copie WORM ; plus lent) |

Pour Splunk, `format: json` (champs riches) ou `format: cef`/`syslog` (formats de ligne
que Splunk parse nativement) fonctionnent tous deux. Le fichier est ouvert en
append-only, donc le même fichier sert aussi de copie externe immuable lorsqu'il est
placé sur un stockage WORM.

:::note[`filelog` transporte les constats, pas le ledger signé]
Le connecteur `filelog` forwarde le flux des **constats** — il ne voit jamais le ledger
d'audit à altération détectable. Pour forwarder le ledger vérifiable, utilisez le flux B.
:::

### Alternative clé en main : Splunk HEC

Si vous préférez pousser en HTTP plutôt que suivre un fichier, le connecteur `splunkhec`
poste le même flux de constats vers le HTTP Event Collector de Splunk
(`/services/collector`) avec un en-tête `Authorization: Splunk <token>` — un chemin HTTP
clé en main, toujours pas du S2S et toujours le flux des constats, pas le ledger.

## Flux B — le ledger à altération détectable, via l'export pull

Le ledger d'audit est exposé comme un **export pull authentifié**, pas comme un fichier
que le moteur écrit de lui-même. Chaque enregistrement porte les champs d'intégrité de
chaîne (`seq`, `prev_hash`, `hash`, `sig`) pour que votre SIEM puisse **re-vérifier la
chaîne de hachages hors ligne** ; les PII ne sont jamais exportées.

```bash
# One-shot full export (CEF). Requires a token with the audit:read permission.
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

Les valeurs de `format` prises en charge sont `cef`, `leef`, `syslog`, `otlp`,
`otlp_envelope`, `otlp_log_record` et `ocsf`. `otlp` est une requête d'export
OTLP/HTTP complète et postable par enregistrement, `otlp_envelope` en est l'alias
exact, et `otlp_log_record` est la projection simple d'un LogRecord par ligne.
Les formats de ligne (`cef`/`leef`/`syslog`) sont diffusés en `text/plain` ;
`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf` sont diffusés en NDJSON
(`application/x-ndjson`), un objet JSON par ligne.

:::note[`ocsf` est OCSF v1.8.0 API Activity]
Les éditions antérieures de cette page notaient que le texte d'erreur du moteur omettait
`ocsf` de la liste annoncée — cet écart a été corrigé en amont ; le résumé et le message
de requête invalide sont tous deux construits à partir du registre de formats du moteur, et nomment donc toujours chaque format accepté.
:::

### Tailing incrémental avec un curseur

L'export pagine la chaîne sans trous par numéro de séquence via `?from=`. Pour garder un
fichier continuellement enrichi afin que l'UF le suive, exécutez un petit job planifié
qui reprend à partir de la dernière séquence vue :

```bash
#!/bin/sh
# cron: every minute. Appends only new ledger records since last run.
STATE=/var/lib/olivares-export/last_seq
OUT=/var/log/olivares/audit.cef
FROM=$(cat "$STATE" 2>/dev/null || echo 1)

curl -fsS "https://localhost:8443/v1/audit/export?format=cef&from=$FROM" \
  -H "Authorization: Bearer $OLVK_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  | tee -a "$OUT" \
  | sed -n 's/.*olivares-audit-export-complete .*last_seq=\([0-9]*\).*/\1/p' \
  | tail -1 > "$STATE.next" && [ -s "$STATE.next" ] && mv "$STATE.next" "$STATE"
```

Chaque export se termine par un terminateur d'achèvement — un commentaire
`# olivares-audit-export-complete count=N last_seq=M` pour les formats texte, ou une
ligne JSON `{"export_complete":true,...}` pour
`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf`. **Son absence signifie que
le flux a été tronqué** — n'avancez pas le curseur s'il manque.

## Pointer l'Universal Forwarder sur le fichier

Quel que soit le flux choisi, installez un UF Splunk sur l'hôte et ajoutez une entrée
`monitor://`. Aucun `inputs.conf` n'est fourni avec Olivares AI — voici la stanza à
ajouter :

```ini
# $SPLUNK_HOME/etc/system/local/inputs.conf
[monitor:///var/log/olivares/audit.cef]
disabled = false
sourcetype = cef
index = olivares_audit

# For the findings file written by the filelog connector:
[monitor:///var/log/olivares/findings.json]
disabled = false
sourcetype = _json
index = olivares_findings
```

L'UF forwarde en S2S vers votre indexeur ; Olivares AI ne parle jamais S2S lui-même.

## Récapitulatif de ce qui est et n'est pas pris en charge

- **Pris en charge :** Forwarding par file-tail (un UF suit un fichier) — pour les
  deux flux.
- **Pris en charge :** Push Splunk HEC — pour le flux des constats (destination
  `splunkhec`) **et** pour le ledger et les constats via un **sink** d'événements
  (`sink_kind: splunk_hec`, événements `audit.recorded` / `finding.reported`, au moins
  une fois) — voir [pousser vers votre SIEM](/fr/how-to/cookbook/push-to-siem/).
- **Pris en charge :** Re-vérification hors ligne du ledger — l'export pull et la pompe push portent
  tous deux les champs de chaîne de hachages mot pour mot, donc un SIEM peut
  re-vérifier l'intégrité.
- **Non pris en charge :** Émetteur natif Splunk S2S — non implémenté (post-v1).
- **Non pris en charge :** Sink *fichier* automatique du ledger — pour faire entrer le ledger dans un
  fichier local, matérialisez-le avec l'export pull planifié ci-dessus (la pompe push
  cible des sinks HTTP, pas des fichiers).
