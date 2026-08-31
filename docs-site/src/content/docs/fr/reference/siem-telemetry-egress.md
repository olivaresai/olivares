---
title: Export SIEM et télémétrie
description: >-
  Tous les formats de fil émis par le plan de contrôle — CEF, LEEF 2.0, syslog
  RFC 5424, logs OTLP, OCSF 1.8.0, SARIF 2.1.0 —, la correspondance de sévérité
  sur laquelle s'appuie une règle, les limites de récepteur propres à chaque
  transport, et les deux endroits où une projection n'est pas une enveloppe
  complète.
---

Cette page est le **contrat de sortie** : ce qui quitte le plan de contrôle, dans
quel dialecte, sur quel transport, et ce qu'un récepteur en fait. Elle est écrite
pour la personne qui doit faire fonctionner du premier coup une règle ArcSight,
un DSM QRadar, une DCR Sentinel ou un téléversement code scanning.

Tout ici est vérifié contre les spécifications des éditeurs eux-mêmes, avec la
date du contrôle. Là où un éditeur **ne** spécifie pas quelque chose, la page le
dit au lieu de deviner : ces trous sont marqués *non défini par l'éditeur*, et
l'encodeur retient à chaque fois l'interprétation conservatrice.

## Les deux flux

Il existe deux sources d'enregistrements indépendantes, et elles partagent un
même encodeur pour que les dialectes ne puissent pas diverger :

| Flux | Contenu | Pull | Push |
|---|---|---|---|
| **Journal d'audit** | Le journal append-only chaîné par hachage, avec ses champs d'intégrité (séquence, hachage précédent, hachage, signature) | `GET /v1/audit/export?format=…` (NDJSON, un enregistrement par ligne) | Le transmetteur du journal, via n'importe quel connecteur de sortie |
| **Notifications et findings** | Findings de gouvernance, décisions de politique, événements de santé et de cycle de vie | — | N'importe quel connecteur de sortie |

Les champs d'intégrité du journal voyagent **verbatim** dans tous les formats : un
SOC peut donc revérifier la chaîne depuis la copie de son propre SIEM, et pas
seulement depuis le produit.

## Formats

| Format | Standard | Version épinglée | Où le sélectionner |
|---|---|---|---|
| CEF | ArcSight Common Event Format | V27 (juillet 2024) | export du journal, connecteurs |
| LEEF | IBM QRadar Log Event Extended Format | 2.0 | export du journal, connecteurs |
| syslog | RFC 5424 (+ RFC 5426 UDP, cadrage TCP RFC 6587, TLS RFC 5425) | — | export du journal, connecteurs |
| Requête OTLP (`otlp`) | Requête d'export OTLP/HTTP JSON (`ExportLogsServiceRequest`) | voir *Projections* ci-dessous | export du journal, connecteurs |
| Requête OTLP (`otlp_envelope`) | Alias exact, octet pour octet, de `otlp` | voir *Projections* ci-dessous | export du journal, connecteurs |
| LogRecord OTLP (`otlp_log_record`) | OpenTelemetry logs, un LogRecord par ligne | voir *Projections* ci-dessous | export du journal |
| OCSF | Open Cybersecurity Schema Framework, profil `ai_operation` | 1.8.0 | export du journal, connecteurs |
| ASIM | Microsoft Sentinel Advanced SIEM Information Model | — | connecteurs |
| ECS | Elastic Common Schema | 9.4.0 | connecteur Elastic |
| UDM | Google SecOps Unified Data Model | — | connecteur Chronicle |
| SARIF | OASIS Static Analysis Results Interchange Format | 2.1.0 Errata 01 | export des findings |

Chaque surface de sélection accepte son propre sous-ensemble ordonné de ces jetons,
dérivé d'un catalogue unique pour que les listes ne puissent plus diverger :

| Surface | Jetons acceptés | Défaut |
|---|---|---|
| Export du journal (`GET /v1/audit/export?format=…`) | `cef\|leef\|syslog\|otlp\|otlp_envelope\|otlp_log_record\|ocsf` | `cef` |
| Sink d'eventing (`sink_format` d'un abonnement push) | `ocsf\|cef\|leef\|syslog\|otlp\|otlp_envelope\|json` | `ocsf` |
| Connecteurs de notification (`filelog`, `splunkhec`, `s3archive`, `siem`) | `json\|cef\|leef\|syslog\|otlp\|otlp_envelope\|ocsf\|asim` | `json` |
| Connecteur syslog | `syslog\|cef\|leef` | `syslog` |

L'export du journal n'a pas de passthrough JSON brut — ses formes JSON sont les
formes OTLP ci-dessus. `json` recouvre deux livraisons différentes : le sink
d'eventing publie l'enveloppe brute de l'événement capturé (le passthrough
structuré, sans transformation de dialecte), tandis que les connecteurs de
notification ne rendent qu'une projection de notification minimale — les champs
affichables, pas la charge utile d'origine. Les quatre connecteurs de
notification acceptent `asim`, `s3archive` compris. Un format hors de la liste
de sa surface est rejeté : une coquille à la rédaction ou à la configuration
reçoit une erreur qui nomme les jetons acceptés de la surface, et une valeur
stockée corrompue est refusée à l'encodage (en nommant la graphie corrompue, pas
la liste) ; rien ne retombe en silence sur JSON.

## Sévérité : la source de vérité

Toute règle qui filtre sur la sévérité s'appuie sur ce tableau. Une seule
correspondance en un seul endroit : le nombre CEF, la priorité syslog et la
sévérité OTLP d'un même événement ne peuvent donc jamais se contredire.

| Sévérité produit | CEF (0-10) | syslog (0-7) | OTLP | ECS | UDM |
|---|---|---|---|---|---|
| info | 1 | 6 (info) | INFO | 1 | INFORMATIONAL |
| low | 3 | 5 (notice) | INFO2 | 3 | LOW |
| medium | 5 | 4 (warning) | WARN | 5 | MEDIUM |
| high | 7 | 3 (error) | ERROR | 7 | HIGH |
| critical | 10 | 2 (critical) | FATAL | 10 | CRITICAL |
| indéterminée | 0 (Unknown) | 6 (info) | UNSPECIFIED | 0 | *(omis)* |

Deux propriétés sont verrouillées par des tests, car toutes deux se perdent
facilement par inadvertance :

- **Les cinq sévérités déterminées ne partagent jamais un nombre.** Un sélecteur
  de collecteur comme `local0.notice`, une règle ArcSight ou une DCR Sentinel
  filtrent sur le nombre émis, et la trame RFC 5424 ne porte aucun autre signal
  de sévérité : deux sévérités partageant une priorité détruiraient une
  distinction en silence et sans retour possible.
- **Une sévérité indéterminée ne s'invente pas.** CEF V27 a renommé le `0` de
  *Low* en *Unknown*, et c'est ce que reçoit un événement sans sévérité
  déterminée. (LEEF fait exception : sa plage est 1-10, sans valeur pour
  « inconnu », d'où le plancher. Voir plus bas.)

:::note[Pourquoi la colonne syslog est ainsi]
Ni CEF ni RFC 5424 ne définissent de correspondance entre sévérité CEF et
priorité syslog — vérifié contre les deux spécifications le 2026-07-24. La
colonne syslog relève donc de la **politique produit**, choisie pour que chaque
sévérité reste distinguable et que « critical » tombe sur la priorité que
RFC 5424 nomme précisément *critical*. La seule correspondance éditeur existante
(un paramètre configurable d'un connecteur ArcSight) place elle aussi sa bande la
plus haute sur `crit`. Si vous avez standardisé un autre découpage, faites la
correspondance sur votre collecteur : ces nombres ne bougeront pas sans une
entrée `Changed` au changelog.
:::

## Spécificités CEF

- **Les tailles d'en-tête** sont bornées aux maxima V27 : device vendor 63,
  device product 63, device version 31, event class id 1023, name 512.
- La spécification publie ces nombres mais ne dit jamais s'ils comptent des
  **caractères ou des octets de fil**, et ne définit aucun comportement pour un
  champ trop long (*non défini par l'éditeur*, vérifié le 2026-07-24). Les deux
  lectures sont donc respectées : une valeur est bornée au nombre en caractères
  décodés **et** en octets UTF-8 sur le fil. Un nom d'équipement ou d'événement
  non-ASCII tient donc en moins de caractères que le nombre ne le suggère — le
  sens conservateur.
- La troncature ne concerne que **l'en-tête**. L'extension, qui porte le contenu
  auditable, n'est jamais tronquée.
- Les clés d'extension à valeur temporelle (`rt`, `start`, `end`) sont en
  **millisecondes epoch** décimales, comme l'exige le dictionnaire CEF.

## Spécificités LEEF

- `sev` est un entier dans la plage **1-10** documentée par LEEF 2.0. Un
  événement dont la sévérité n'a jamais été déterminée sort en `sev=1` : LEEF n'a
  pas de valeur « inconnu » et `sev=0` est hors plage.
- `devTime` est un **epoch à 13 chiffres**, que QRadar accepte sans
  `devTimeFormat`. Il est **omis** — jamais fabriqué — pour un événement sans
  heure enregistrée, et QRadar retombe alors sur l'heure de réception, comme
  documenté.
- `sev`, `devTime` et `devTimeFormat` **appartiennent à l'encodeur**. Si un
  événement porte un champ de l'un de ces noms (quelle qu'en soit la casse), il
  est émis renommé en `olvSev` / `olvDevTime` / `olvDevTimeFormat` : la valeur
  vous parvient toujours, mais elle ne peut ni écraser la sévérité normalisée ni
  redater l'événement. IBM documente qu'un `devTime` reconnu prime sur
  l'horodatage syslog : c'est pourquoi cela n'est pas laissé au hasard.

:::caution[Non défini par IBM]
IBM ne documente pas ce que QRadar fait d'un `sev=0`, d'un `devTime` illisible,
ni si les clés d'attribut sont sensibles à la casse (vérifié le 2026-07-24). Ce
qui précède est la lecture conservatrice de chaque cas. Si vous disposez de
preuves côté récepteur qui disent le contraire, cela mérite une issue.
:::

## Transport syslog et limites de récepteur

Le connecteur syslog transporte un enregistrement RFC 5424 natif, ou un
enregistrement CEF / LEEF comme MSG d'une trame RFC 5424 conforme — c'est
exactement ainsi qu'ArcSight et QRadar ingèrent ces formats via syslog.

- **TLS sur 6514 (RFC 5425) est le défaut**, avec cadrage par comptage d'octets
  comme l'exige la RFC. TCP ou UDP en clair est un renoncement explicite de
  l'opérateur ; aucun chemin de code ne rétrograde une destination TLS en clair.
- **Budget de charge utile du récepteur** (`max_payload_bytes`, défaut `0` =
  désactivé). Un récepteur qui scinde un enregistrement trop grand transforme un
  événement auditable en deux moitiés illisibles. Lorsque vous déclarez le budget
  de la destination que vous exploitez, un enregistrement au-dessus **fait échouer
  la livraison** — réessayée, puis mise en DLQ, où vous la voyez — au lieu d'être
  envoyé pour être scindé. L'enregistrement lui-même n'est jamais tronqué.

Valeurs de référence pour ce réglage, avec ce que dit réellement chaque source
(vérifié le 2026-07-24) :

| Récepteur | Octets | Ce que dit la source |
|---|---|---|
| Tout récepteur RFC 5424 | 480 | Le minimum qu'un récepteur **DOIT** accepter (§6.1) |
| Tout récepteur RFC 5424 | 2048 | La taille que les implémentations **DEVRAIENT** accepter |
| Démon syslog ArcSight | 1024 | Ses guides disent qu'un message plus long **« pourrait être scindé »** — une mise en garde de déploiement, pas une règle de récepteur, et sans effet sur les chemins fichier ou pipe |
| QRadar TCP | 4096 | La charge utile maximale **par défaut** ; augmentable (IBM documente 8192, plafond 32000) |

Aucune de ces sources ne précise si le compte inclut l'en-tête syslog : le budget
est donc mesuré sur l'**enregistrement complet** en octets UTF-8.

## OCSF

Les événements sont émis en OCSF **1.8.0** avec le profil `ai_operation`, dans
les trois classes qui l'enregistrent : API Activity (6003, la classe par défaut),
Process Activity (1007) et Datastore Activity (6005). La sortie est validée dans
la suite de tests contre les schémas de classe officiels 1.8.0, qui interdisent
les champs inconnus : un attribut hors profil ou un objet de profil incomplet
casse donc la build au lieu de vous parvenir.

:::caution[AWS Security Lake accepte OCSF ≤ 1.3]
Une source personnalisée Security Lake plafonne à **OCSF 1.3 en Parquet** : les
événements `ai_operation` 1.8.0 n'y atterrissent donc pas tels quels (vérifié le
2026-07-24). En attendant un émetteur de rétrogradation en 1.3, routez vers
Security Lake via votre propre transformation, ou utilisez une autre destination.
C'est un manque déclaré, pas un oubli.
:::

## Des projections qui ne sont pas des enveloppes

Deux limites assumées, toutes deux bonnes à connaître avant de pointer un
collecteur :

- **`otlp` est la requête publiable sur toutes les surfaces ; `otlp_log_record` est
  la projection nue.** Depuis le remap du catalogue de formats, une ligne
  d'ÉVÉNEMENT `otlp` est une requête d'export OTLP/HTTP JSON complète
  (`ExportLogsServiceRequest`) partout où le jeton est accepté — export du journal,
  connecteurs de sortie, push d'eventing —, avec l'identité de ressource et le scope
  d'instrumentation attendus par un collecteur. `otlp_envelope` est un alias exact,
  octet pour octet, de `otlp` sur toutes les surfaces, conservé parce que cette
  graphie a livré l'enveloppe la première : les deux ne diffèrent jamais. La
  projection à un LogRecord par ligne — un objet JSON par ligne, pour une
  consommation fichier / NDJSON — existe toujours, sous son nom honnête
  `otlp_log_record`, et seulement sur l'export pull du journal : une ligne LogRecord
  nue n'est pas un corps publiable vers `/v1/logs`, les surfaces push ne l'offrent
  donc délibérément pas. Trois précisions, faute de quoi elles coûtent un
  après-midi : la DERNIÈRE ligne du pull est le marqueur
  `{"export_complete":true,…}` d'Olivares et n'est **pas** une requête — une boucle
  qui poste chaque ligne doit la sauter, en filtrant sur la STRUCTURE, par ex.
  `jq -c 'select(has("resourceLogs"))'`, jamais par sous-chaîne : un événement dont
  l'acteur ou la cible contient `export_complete` disparaîtrait avec un `grep -v`, ce
  qui supprime une preuve au lieu d'ignorer un marqueur ; un sink de push doit viser
  l'URL exacte `/v1/logs` du collecteur, l'endpoint étant posté tel quel ; et le
  sink HTTPS générique déclare livré tout 2xx sans lire la réponse de succès partiel
  du collecteur — le **connecteur logs OTLP**, lui, la lit. `otlp_log_record`
  conserve les octets exacts que produisait le jeton `otlp` d'avant le remap dans le
  domaine temporel normal — le temps zéro et tout instant de l'époque jusqu'à
  `2262-04-11T23:47:16.854775807Z`. En dehors, la compatibilité octet à octet n'est
  PAS garantie, et là où les octets diffèrent c'est une correction : une date
  antérieure à l'époque produisait une valeur négative dans un champ qu'OTLP déclare
  non signé, une date entre les plafonds signé et non signé porte désormais sa vraie
  valeur non signée, et une date postérieure à `2554-07-21T23:34:33.709551615Z` est
  désormais encodée comme inconnue (`0`) au lieu d'une valeur débordée — dont les
  petites valeurs positives qui se lisent comme début 1970. Sur des entrées isolées
  débordant à zéro, les anciens et nouveaux octets coïncident. Deux notes de mise à
  niveau, dites clairement : le *fichier* du pull reste du NDJSON (une requête par
  ligne plus un marqueur de fin), pas une requête unique ; et un abonnement
  d'eventing stocké dont le format était écrit exactement `otlp` avant le remap
  livre désormais l'enveloppe là où il livrait une ligne nue — le moteur journalise
  un avertissement structuré par abonnement concerné, et les métadonnées d'audit
  enregistrées avant le remap se lisent avec l'ancien sens du jeton.
- **L'extension de trace OWASP Agentic AI Security** voyage dans le conteneur
  `unmapped` d'OCSF, qui est l'emplacement prescrit par sa spécification (v0.1,
  public preview). Ce n'est pas un jeu d'attributs OCSF de premier rang, et la
  validation de schéma ne couvre que son emplacement.

## Les findings en SARIF

Les findings de gouvernance s'exportent en **SARIF 2.1.0 Errata 01** pour un
consommateur de code scanning :

- `GET /v1/m/security/findings/export?format=sarif` — les mêmes filtres que la
  liste des findings, avec un plafond de résultats et un en-tête de troncature
  honnête lorsqu'il est atteint.
- `olivares findings export` — le même export en CLI, écrit atomiquement avec des
  permissions `0600`.

Le run déclare la base d'URI contre laquelle ses emplacements se résolvent, porte
un `partialFingerprints.primaryLocationLineHash` stable par finding pour qu'un
consommateur déduplique au lieu de réalerter, et refuse d'émettre un résultat
avec un rule id vide ou un level hors énumération : ce sont les deux choses qui
font rejeter le fichier entier, et l'apprendre au téléversement est pire que
l'apprendre ici.

Les findings dont le sujet n'est pas un fichier versionné reçoivent une URI
d'emplacement synthétique. Le run reste valide et ingérable, mais GitHub n'affiche
d'alertes que pour les URI correspondant à un fichier du checkout : un détecteur
qui veut l'ancrage GitHub doit donc fixer l'URI d'artefact explicitement.
