---
title: "Dépannage (symptôme → diagnostic → correctif)"
description: >-
  Le guide des modes de défaillance pour l'opérateur, distillé à partir des
  propres runbooks du produit : problèmes de démarrage et de premier
  lancement, échecs de readiness, contre-pression à l'ingestion, échecs de
  vérification du ledger, et les avertissements que le moteur affiche à
  dessein.
---

Chaque entrée suit la même forme : le symptôme que vous observez, comment confirmer ce que
c'est, et le correctif. Les lignes de log citées sont les chaînes réelles du moteur, vous
pouvez donc les rechercher avec grep. Lorsqu'un runbook plus approfondi existe, l'entrée pointe
vers la page concernée plutôt que de la re-dériver.

## Premier démarrage et amorçage

### J'ai raté le jeton d'amorçage

Un redémarrage ne le ré-affiche **pas** (seul le hachage du jeton est stocké, dans
`setup.token` dans le répertoire de données). Tant qu'aucun utilisateur n'existe encore, la
récupération est sûre : arrêtez le moteur, supprimez `setup.token`, démarrez-le — un nouveau
jeton est généré et affiché. Cela ne fonctionne *que* sur une installation sans utilisateur,
ce n'est donc pas un chemin de prise de contrôle. Le jeton va **uniquement sur stdout** (le
journal sous systemd, le log du conteneur sous Docker/Kubernetes) — jamais dans des fichiers
de log.

### `=== FIRST-BOOT SETUP ===` n'est jamais apparu

Des utilisateurs existent déjà dans ce répertoire de données — vous n'êtes pas au premier
démarrage. Soit connectez-vous avec l'administrateur existant, soit, pour un démarrage
réellement neuf, utilisez un `--data-dir` frais.

### Le moteur avertit à propos des clés au premier démarrage

```text
generated a new audit signing key; back it up path=/var/lib/olivares/audit-signing.key
generated a self-signed TLS certificate; clients must trust it, or pin it with --pin-sha256=<pin_sha256> (that value, verbatim) cert=/var/lib/olivares/tls.crt cert_fingerprint_sha256=d38567e8…378c4e7f pin_sha256=JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

Les deux sont délibérés, et le premier est celui qui se retourne contre vous plus tard : il n'y
a **aucun séquestre imposé** — copiez `audit-signing.key` hors de la machine dès maintenant, et
épinglez la clé publique (`GET /v1/audit/pubkey`) hors de la machine, sinon une compromission
future de l'hôte vous laisse incapable de prouver votre propre ledger
([sauvegarde & restauration](/fr/how-to/backup-and-restore/#les-deux-clés-qui-décident-de-tout)).

La ligne TLS imprime **deux** condensats, et ils ne sont pas interchangeables :
`cert_fingerprint_sha256` est le condensat du certificat, celui qu'affiche un
navigateur ; `pin_sha256` est le condensat du SPKI du certificat feuille, et
c'est le seul que `--pin-sha256` compare. Copiez cette valeur telle quelle :

```bash
olivares status --server https://127.0.0.1:8443 \
  --pin-sha256 JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

Épingler l'empreinte du certificat à la place n'échoue pas comme une valeur
d'option invalide — c'est un condensat de 32 octets bien formé, la connexion est
donc tentée puis refusée avec `TLS SPKI pin mismatch`, qui indique la valeur
qu'il fallait utiliser. Avec `curl --pinnedpubkey sha256//…`, ajoutez le
remplissage `=` final : le moteur imprime du base64 sans remplissage
délibérément, pour que la valeur s'affiche sans guillemets dans le journal et
survive à un copier-coller, mais curl exige la forme complétée.

## Diagnostic d’installation de l’hôte

La référence CLI générée décrit `olivares doctor` ainsi : diagnose this host
installation without printing secrets
([CLI](/reference/cli/#command-olivares-doctor)). Les flags comprennent `--data-dir`, `--mode` (`auto` \| `user` \| `system`), `--init` (`auto` \| `systemd` \| `openrc` \| `launchd`), `--config`, `--unit`, `--binary`, `--server`, `--timeout`, `--ca-cert`, `--audit-tenant`, `--check-updates`.
Ne traitez pas cette table comme un texte d’aide supplémentaire ; c’est cette
commande générée.

### `agentops-layout` check

`olivares doctor` rapporte une vérification nommée `agentops-layout`
(`CHANGELOG.md` `[26.9.0]` ; `cmd/olivares/cmd_doctor.go` `doctorAgentOpsCheck`).
Ce n’est pas une sous-commande. Elle mesure la disposition AgentOps native que
le manifeste de propriété enregistre : le drop-in géré doit exister avec son
mode et doit nommer le `HOME` claude, le répertoire de jetons et l’espace de
travail enregistrés ; l’env d’exécution doit exister avec un mode de
configuration (les valeurs ne sont jamais lues) ; l’espace de travail doit être
un répertoire (`docs/RELEASE-INSTALLER.md`).

Statuts renvoyés par la vérification :

| Status | When |
|---|---|
| `not_applicable` | no readable manifest; malformed manifest; or the manifest records no AgentOps files (`dropin`, `runtime-env`, `workspace_dir` all empty) |
| `fail` | drop-in does not reference a recorded token (`$dataDir/claude-home`, `$dataDir/run`, or `workspace_dir`); workspace path is absent or is not a directory |
| `unknown` | drop-in or workspace is not readable |
| `pass` | drop-in, runtime-env (values not read) and workspace directory agree with the record |

Required est false jusqu’à ce que le manifeste enregistre au moins un fichier
AgentOps ; alors la vérification est required. Les chaînes de remédiation que
le moteur imprime comprennent
`rerun install-agentops.sh; the managed drop-in and the recorded layout disagree`
et `recreate the recorded workspace or rerun install-agentops.sh with OLIVARES_WORKSPACE_DIR`.

Le texte et `-o json` contiennent des noms de clés et des chemins, jamais des
valeurs de configuration (`docs/RELEASE-INSTALLER.md`).

## Sources et l'access map

### La carte est vide

Vérifiez d'abord si quelque chose est câblé. Le moteur le dit explicitement au démarrage :

```text
ingest: no observation sources configured (OLIVARES_SOURCES_CONFIG.sources is empty); no connector will ingest — the estate runs on no live traffic
```

Un fichier de sources manquant, illisible ou invalide **avertit et continue** (le démarrage ne
plante jamais à cause de cela) — donc un moteur d'apparence saine avec une carte vide signifie
généralement que la config n'a jamais été chargée. Corrigez le fichier/chemin et redémarrez ; le
succès ressemble à `ingest: wired source … kind=…` par source. Une source qui échoue à se
construire journalise `ingest: failed to register in-process source; not wired` avec la raison —
c'est rapporté, jamais abandonné silencieusement.

### pgAudit est câblé mais aucune arête n'arrive

Trois causes couvrent presque tous les cas, toutes par conception
([le guide pgAudit](/fr/how-to/connectors/pgaudit/)) :

1. **Le serveur ne journalise pas en UTC.** Les enregistrements avec une abréviation de fuseau
   horaire non-UTC sont **ignorés** plutôt que mal horodatés — définissez
   `log_timezone = 'UTC'`.
2. **csvlog est en lot, pas en tail.** `follow` ne s'applique qu'à `jsonlog` ; une source csvlog
   ingère à chaque passe, pas en continu.
3. **Les classes auditées sont désactivées** — vérifiez que `pgaudit.log` inclut
   `read, write`.

### Tout apparaît comme du drift

Attendu sur une installation fraîche : sans grant déclaré, chaque accès observé est honnêtement
« inattendu ». C'est l'état de départ, pas un bug —
[triez-le](/fr/how-to/cookbook/drift-triage/) en déclarant les grants que vous comptez accorder.

## Disponibilité

### `/readyz` renvoie 503

Lisez le corps. Un 503 n'est pas ready et n'est pas un succès. Que `/livez`
reste 200 signifie seulement que le processus tourne ; que `/pod-readyz`
reste 200 signifie seulement la santé du pod (magasin joignable) sans
contrôle de leadership ni sonde de capacité de premier démarrage. Ni l'un
ni l'autre ne débloque le chart Helm par défaut ni le manifeste plat, qui
câblent `/readyz` comme `readinessProbe` du conteneur.

- `{"status":"unavailable","store":"down"}` — le magasin est injoignable. Sur SQLite : disque
  plein, problèmes de PVC, permissions de fichier. Sur Postgres : joignabilité et identifiants.
  **La liveness continue délibérément de passer** (le processus est vivant), de sorte que rien
  ne boucle en redémarrage lors d'une panne du magasin ; redémarrez le pod/service manuellement
  après avoir corrigé le magasin s'il reste bloqué.
- `{"status":"standby","store":"up","leader":false}` — un standby HA qui répond honnêtement. Pas une
  erreur : le Service route vers le leader ; les standbys se vident par conception. Si **tous**
  les réplicas se déclarent standby, l'élection du leader est bloquée — vérifiez la connectivité
  du verrou consultatif (advisory-lock) Postgres.
- `{"status":"setup_blocked","store":"up","leader":true,"setup_required":true,"code":"cross_tenant_admin_pool_not_configured"}`
  — le premier démarrage PostgreSQL ne peut pas énumérer les organisations de
  façon autoritative. Provisionnez le rôle administratif `NOSUPERUSER BYPASSRLS`
  et `--admin-dsn` ([Postgres sur Kubernetes](/fr/tutorials/getting-started/kubernetes/#2-postgres-multi-tenant)).
  Le corps porte un remède fixe. Tant que ce prérequis n'est pas corrigé, la
  sonde de readiness par défaut maintient le pod Not Ready. Ne reconfigurez
  pas la sonde pour masquer cela.
- `{"status":"setup_unavailable","store":"up","leader":true,"code":"setup_state_unavailable"}`
  — ce nœud n'a pas pu observer si l'installation est configurée.
  `setup_required` est **omis** parce que sa valeur est inconnue. Ce n'est
  pas une installation vide connue.
- `{"status":"setup_unavailable","store":"up","leader":true,"setup_required":true,"code":"setup_probe_unavailable"}`
  — l'installation est connue comme non configurée, mais la sonde de capacité
  de premier démarrage a échoué pour une autre raison que le refus du pool
  administratif. Un timeout n'est pas la preuve d'un parc vide.

Un 200 `{"status":"ok",…,"setup_required":true}` est une autre observation :
le setup de premier démarrage peut être tenté. `POST /v1/setup` continue de
revérifier son autorité. Ce 200 ne consomme pas le budget de disponibilité ;
ces 503 en consomment.

### Le pod est mort et rien n'a pris le relais

Sur la topologie **par défaut à réplica unique**, il n'y a pas de basculement automatique — la
récupération est le replanning du StatefulSet plus le ré-attachement du volume RWO (surveillez
les erreurs Multi-Attach ; le volume épingle la récupération à sa zone de disponibilité). Le
basculement automatique est une propriété de la
[topologie HA](/fr/tutorials/getting-started/kubernetes/#3-ha-active-passive)
(Postgres + réplicas + clé de signature partagée). Ne faites jamais tourner la production avec la
persistance désactivée : un `emptyDir` perd la clé de signature à chaque replanning.

## Performance

### La latence d'ingestion p99 augmente (contre-pression)

Le bus **bloque plutôt que d'abandonner** — une hausse de
`olivares_ingest_duration_seconds` p99 est le signal voulu indiquant qu'un abonné est saturé, pas
une perte de données. Désignez le coupable directement :

```promql
olivares_eventbus_queue_depth / olivares_eventbus_queue_capacity > 0.9
```

Les labels par abonné pointent vers le module lent ;
`olivares_eventbus_publish_blocked_total` compte les événements de contre-pression. La cause
racine habituelle est le **débit d'écriture du magasin** (le plafond d'écrivain unique de
SQLite) — c'est un correctif de capacité (passer à Postgres, ou réduire l'amplification
d'écriture), pas un bouton de réglage. Les connecteurs de sortie lents (un webhook, un SIEM) ne
doivent jamais être des abonnés synchrones.

Avec le bus distribué activé (`OLIVARES_BUS_CONFIG`), rappelez-vous que le pont inter-nœuds est
**au-plus-une-fois** : un pont saturé remplit
`olivares_eventbus_bridge_pending_messages` puis **abandonne les événements distants**, comptés
dans `olivares_eventbus_bridge_dropped_total` — alertez sur toute augmentation, et déclenchez une
astreinte quand `olivares_eventbus_bridge_connected == 0`.

### Les connexions échouent avec « locked out »

`olivares_auth_login_attempts_total{outcome="locked_out"}` en hausse signifie que la limitation
a refusé une tentative : un compte verrouillé par ses propres échecs répétés, ou un compte
qu'une adresse client suivie d'une série d'échecs essayait déjà. Un compte au dossier vierge
n'est jamais verrouillé par les échecs d'autrui depuis la même adresse — il attend une
seconde à la place. Les deux se libèrent d'eux-mêmes ; enquêtez sur la source des échecs
plutôt que de relever les limites.

## Preuves

### Le ledger échoue à la vérification

D'abord, sachez ce que vous avez exécuté : le `audit verify` par défaut **se termine avec le code
0 même sur une chaîne en échec** (le résultat est dans le rapport JSON) — l'automatisation doit
utiliser `--strict` ou analyser le rapport :

```bash
olivares audit verify --tenant $TENANT --data-dir /var/lib/olivares --strict \
  --pubkey <BASE64-PINNED-OFF-BOX>
```

Épinglez la clé publique **hors machine** : sans épinglage, le vérificateur fait confiance aux
clés lues depuis l'hôte (potentiellement compromis) — acceptable comme contrôle indicatif, pas
comme preuve d'altération. Ensuite, classez selon le champ `reason` :

| Raison | Classe | Réponse |
|---|---|---|
| `hash-mismatch`, `prev-mismatch`, `head-mismatch`, `tail-truncated` | altération ou troncature | traitez comme un SEV1 : préservez la machine, réconciliez avec le checkpoint hors machine |
| `checkpoint-sig-invalid`, `checkpoint-link-mismatch`, `event-sig-invalid` | altération ou mauvaise clé | SEV1 sauf si vous pouvez prouver une confusion de garde de clé |
| `seq-gap` | suppression **ou** incohérence de restauration | comparez au checkpoint hors machine avant de crier à l'altération |
| `event-sig-missing` | possiblement d'anciens enregistrements antérieurs à l'activation de la signature | bornez-le avec `--from` à la limite d'activation ; l'absence pré-limite est attendue |

Une sauvegarde restaurée qui passe un parcours naïf mais diverge de votre checkpoint hors machine
épinglé est le cas d'anomalie de restauration — cette comparaison est la raison d'être de
l'épinglage.

### `olivares_audit_checkpoint_age_seconds` ne cesse de croître

Les checkpoints ont cessé d'être écrits (cadence par défaut 1h ;
`OlivaresAuditCheckpointStale` se déclenche à 2h). Vérifiez le log du moteur pour des erreurs de
checkpoint et la capacité d'écriture du magasin — pendant qu'il croît, votre ancre de preuve
d'altération vieillit.

## Notifications et sinks

### Une destination ne reçoit jamais rien

Une destination dont le kind est inconnu est **ignorée et journalisée**
(`notify: destination has unknown connector kind; skipped` — vérifiez l'orthographe du
`kind`). Pour les sinks d'eventing, `POST …/subscriptions/{id}/test` envoie une livraison que
vous pouvez observer, et les endpoints doivent être en HTTPS
([pousser vers un SIEM](/fr/how-to/cookbook/push-to-siem/)).

---

Si un symptôme n'est pas ici et que le propre message du moteur ne l'explique pas, c'est un bug
de documentation — merci de le signaler avec la ligne de log.
