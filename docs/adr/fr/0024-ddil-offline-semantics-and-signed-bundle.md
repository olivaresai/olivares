> Traduction automatique. La version anglaise fait foi.

# ADR-0024: Sémantique offline DDIL par plan et format unique de bundle signé

- **Status:** accepted
- **Date:** 2026-07-09
- **Deciders:** Fran Olivares (ratified the three questions below during design, 2026-07-09)
- **References:** the DDIL work brief; the OTA update framework
  (`core/release/manifest.go`); ADR-0009
  (hash-chained audit ledger); ADR-0013 (embedded Cedar PDP); ADR-0021 (durable
  JetStream bus, enterprise add-on); ADR-0022 (source scoping, forbid-overrides);
  the durable bus seam (`core/eventbus`, ADR-0021); break-glass.

## Contexte et énoncé du problème

Olivares est déployé à l'edge tactique / déconnecté (DDIL du DoD : « s'attend à ce que les
unités opèrent au moins partiellement déconnectées… sur des réseaux air-gapped… et à l'edge
tactique »). L'acheteur de l'edge ne nous demande pas « d'intégrer une liaison satellite » —
un bearer pLEO/satellite n'est qu'une connexion IP intermittente, et l'application
fonctionne dessus sans changement. Il exige que la gouvernance continue de fonctionner
lorsque la liaison est interrompue pendant des heures ou des jours et revient lors de
courtes fenêtres (« remontée du sous-marin à la surface »).

Les composants de base existent déjà et ont été vérifiés pendant la découverte :

- Le **ledger d'audit est déjà un store local durable, par tenant, chaîné par hash et signé**
  (`core/internal/store/sqlstore/audit.go` ; ADR-0009). La déconnexion n'y crée aucune
  lacune — elle empêche simplement le **curseur de forwarding** hors équipement
  (`modules/siemforward`, piloté par la plateforme d'eventing) d'avancer. Aucun tampon
  d'audit exclusivement en RAM ne peut être perdu.
- Le **PDP évalue par rapport au store LOCAL de politiques** (Cedar embarqué, ADR-0013) ; la
  politique fonctionne donc déjà offline. La question non tranchée concerne son
  *obsolescence* : combien de temps un nœud déconnecté peut-il continuer à faire confiance
  à une politique qu'il ne peut plus actualiser ?
- Le **bus durable** est un overlay JetStream réservé au leader et at-least-once
  (ADR-0021), dont le backend est une compilation enterprise privée ; l'arbre OSS ne livre
  que la jointure. Il s'agit d'un backbone de *distribution*, pas d'un spool local sur
  disque.
- **L'updater OTA définit déjà un bundle signé** pour les mises à jour air-gap : un tar gzip
  d'un `manifest.json` JSON accompagné d'une signature Ed25519 détachée sur les octets
  littéraux séparés par domaine (`tag || manifest`, tag
  `olivares.update-manifest.v1\n`), vérifiée AVANT l'analyse
  (`core/release/manifest.go`). Un `airgap-bundle.sh` distinct (cosign, images + chart) et
  `core/dr/bundle.go` (snapshot DR scellé par AES-GCM) existent également.

Trois questions doivent être tranchées avant d'écrire le moindre code DDIL, car elles
définissent le sens fail-safe, pas le mécanisme.

## Facteurs de décision

- **Fail-safe dans le bon sens.** Un plan de contrôle de gouvernance ne doit jamais
  *élever* des privilèges parce qu'il a perdu sa liaison, ni *perdre silencieusement* des
  preuves.
- **Sécurité de la mission à l'edge.** Une interruption de liaison mesurée en heures ne
  doit pas mettre fin à la mission lorsque la réponse sûre était déjà connue localement.
- **Aucune prolifération de formats.** « Un format de bundle vérifiable, pas deux » (brief
  de conception DDIL). Une seconde implémentation artisanale d'enveloppe signée est un
  second endroit où se tromper sur la séparation des domaines — exactement le piège de la
  réutilisation de clés entre protocoles déjà traité par l'updater OTA.
- **Honnêteté.** Des limites déclarées et documentées (budgets disque, TTL, ce qui ne survit
  pas à une interruption infinie) plutôt qu'une troncature silencieuse.

## Options envisagées

### Q1 — Confiance offline dans la politique

- **A. Asymétrique (deny perpétuel, allow expire).** Les règles restrictives (deny ABAC,
  `forbid` Cedar) restent appliquées indéfiniment offline ; les grants positifs (`allow`
  Cedar scopé, ADR-0019/ADR-0022) expirent après un `policy_max_staleness` signé et échouent
  deny-closed.
- **B. Deny-closed total à l'expiration du TTL.** Après le TTL, le nœud cesse entièrement
  de gouverner.
- **C. Ne jamais expirer, avertir uniquement.**

### Q2 — Comportement de l'audit lorsque le budget disque local est épuisé

- **A. Fail-closed par défaut, dégradation opt-in.** `block` par défaut : refuser les
  nouvelles actions gouvernées avant de perdre des preuves. `degrade` opt-in : sceller le
  segment et ajouter un **marqueur de lacune signé et intégré à la chaîne**, afin que la
  perte soit détectable, et ne soit jamais silencieuse.
- **B. Toujours fail-closed.**
- **C. Toujours dégrader.**

### Q3 — Unification du format de bundle

- **A. Extraire `core/sigbundle` + un registre de tags de domaine.** Remonter l'enveloppe de
  mise à jour OTA dans un package partagé ; refactoriser `core/release` pour la consommer
  derrière un test golden d'identité des octets ; ce travail DDIL et le feed de security
  advisories ajoutent leurs propres tags de domaine.
- **B. Laisser `core/release` en l'état ; chaque session copie le modèle.**

## Résultat de la décision

**Q1 → Option A (asymétrique).** Offline, au-delà de `policy_max_staleness` :

| Classe de règle | Offline, TTL expiré | Justification |
|---|---|---|
| Deny ABAC | **toujours appliqué** | une restriction obsolète ne peut que restreindre, jamais élever les privilèges |
| `forbid` Cedar (absolu, ADR-0022) | **toujours appliqué** | même raison ; forbid prime déjà sur tout |
| Grant positif / `allow` Cedar | **expiré → deny-closed** | « un grant expiré ne doit jamais autoriser » |
| Break-glass | disponible, avec sa propre expiration de 1 h/24 h | la voie de secours offline sanctionnée |

`policy_max_staleness` est un paramètre de l'opérateur (72 h par défaut), transporté et
signé dans le bundle de politiques ; la console/CLI affichent clairement l'âge et
l'expiration.

**Q2 → Option A (fail-closed par défaut, dégradation opt-in).** Configuration
`audit.spool.on_full` :

- `block` (par défaut) : les nouvelles actions gouvernées sont refusées (`503`,
  deny-closed) ; les lectures continuent d'être servies ; la console/CLI affichent
  « audit spool full — governance halted ».
- `degrade` (opt-in explicite) : scelle le segment actuel et ajoute un marqueur
  `audit.gap` signé et intégré à la chaîne
  `{from_seq, to_seq, reason: "spool_full", count, at}`, afin que la chaîne reste continue
  et que la perte soit prouvable. `audit.spool.max_bytes` est déclaré et documenté.

Le marqueur de lacune est la SEULE discontinuité sanctionnée de la chaîne ; le vérificateur
d'archives offline (`core/audit/archiveverify.go`) est étendu pour reconnaître un marqueur
de lacune signé comme une frontière *déclarée*, plutôt qu'une erreur `seq-gap`.

**Q3 → Option A (extraire `core/sigbundle`).** Une seule enveloppe :

```
core/sigbundle/
  SigningInput(tag, payload) = tag || payload           // verbatim, no canonicalization
  Sign(tag, payload, priv) / Verify(tag, bundle, sig, pub)   // Ed25519, detached, verify-BEFORE-parse
  Envelope: tar.gz{ manifest.json, manifest.json.sig, <payload files by sha256> }
  Manifest: schema_version, kind, created_at, expires?, entries[{name, sha256, size}]
```

`core/release` est refactorisé pour réutiliser `sigbundle.SigningInput` avec le tag
`olivares.update-manifest.v1\n`, protégé par un test golden affirmant que
`release.ManifestSigningInput(b)` reste identique octet pour octet (afin que toutes les
signatures de publications déjà émises continuent d'être vérifiées). Le **registre des tags
de domaine** (une table + un test d'unicité/absence de collision de préfixes) consigne chaque
tag :

| Tag | Propriétaire | Note |
|---|---|---|
| `olivares.update-manifest.v1\n` | `core/release` (update manifest) | identique octet pour octet après refactorisation |
| `olivares.ddil-bundle.v1\n` | ce travail DDIL | NOUVEAU — bundle air-gap politique+audit+preuves |
| `olivares.security-advisories.v1\n` | feed de security advisories | NOUVEAU — feed signé d'advisories OSV |

`core/license` (payload JSON brut commençant par `{`) et les domaines des événements/
checkpoints d'audit (`olivares.audit.*`) restent manifestement disjoints de chaque tag (un
tag ne commence jamais par `{`, et les domaines d'audit sont des préimages préfixées par
leur longueur, pas des bundles tar). `core/dr/bundle.go` est intentionnellement **laissé en
l'état** : il s'agit d'un snapshot DR *scellé* (AES-GCM) et non signé — un modèle de
confiance différent (confidentialité, et non authenticité de l'éditeur) — et l'intégrer
confondrait les deux.

### Conséquences

- **Avantages :** fail-safe dans le bon sens sur les deux plans ; une seule enveloppe
  auditée et une seule discipline de séparation des domaines au lieu de trois ; l'edge
  continue de refuser ce qui a toujours été refusé, même après une longue interruption ;
  la perte de preuves est impossible par défaut et reste détectable lorsqu'elle est
  explicitement autorisée.
- **Inconvénients / compromis :** les grants positifs cessent de fonctionner après
  `policy_max_staleness` lors d'une interruption réellement longue (atténué par break-glass
  et par le choix du TTL laissé à l'opérateur) ; le mode `degrade` échange des preuves
  contre de la disponibilité et doit être choisi consciemment ; la refactorisation de
  `core/release` touche le code fraîchement fusionné de l'updater OTA (atténué par le test
  golden d'identité des octets).
- **Neutre / suivis :** le feed de security advisories dépend de `core/sigbundle` et de son
  propre tag ; le vérificateur d'archives gagne un vocabulaire `declared-gap` ;
  `docs/deploy/ddil.md` documente les budgets disque, le TTL et ce qui ne survit pas à une
  interruption infinie.

## Pourquoi les alternatives ont été rejetées

- **Q1-B (deny-closed total) :** met fin à la mission. Une liaison coupée plus longtemps que
  le TTL arrêterait une unité à l'edge, alors que ses règles deny n'ont jamais été mises en
  doute.
- **Q1-C (ne jamais expirer) :** un grant révoqué au centre resterait actif à l'edge pour
  toujours — une fenêtre d'autorisation illimitée est inacceptable pour un plan de
  gouvernance.
- **Q2-B (toujours fail-closed) :** supprime un compromis légitime de l'opérateur (certaines
  missions à l'edge ne doivent pas s'arrêter) ; le marqueur de lacune signé rend déjà la
  dégradation honnête.
- **Q2-C (toujours dégrader) :** choix par défaut trop faible pour un produit de gouvernance
  — la perte de preuves silencieuse par politique est précisément ce que le ledger doit
  empêcher.
- **Q3-B (copier le modèle) :** trois implémentations d'enveloppe et trois occasions de
  rater la séparation des domaines ; la leçon de la réutilisation de clés entre protocoles
  était justement qu'une clé appliquée à deux types de messages sans tag crée un vecteur de
  falsification.

## Note d'implémentation (2026-07-10)

Q2 est implémentée conformément à la décision ratifiée. Le marqueur de lacune déclare la
plage abandonnée `{from_seq, to_seq, count, reason, at}` comme un trou de séquence dont le
chaînage de hash reste continu ; le vérificateur de chaîne live, l'exporteur d'archives et
le vérificateur d'archives offline reconnaissent tous un marqueur correctement déclaré et
correctement signé comme une frontière déclarée (`declared_gaps` dans leurs rapports), tout
en continuant d'échouer sur toute discontinuité non déclarée ou incohérente. Le budget
mesure les octets logiques exacts des valeurs d'événements stockées au moyen d'un compteur
incrémental recalculé depuis le ledger à chaque boot soumis au budget ; les mécanismes
d'intégrité (checkpoints, ancres d'archives, marqueur lui-même) sont admis au-delà du budget,
mais entièrement comptabilisés, et le plan système est soumis au budget comme tout autre
writer.

Une implémentation parallèle qui conservait la chaîne sans lacune (marqueur récapitulatif
sans trou de séquence, mesure physique des pages/relations, exemption du plan système) a été
intégrée le même jour puis remplacée par celle-ci lors de la réconciliation : le texte
ratifié spécifie la plage déclarée et l'extension du vérificateur, tandis que le compteur
exact supprime l'hystérésis de mesure et les problèmes de migration v3 modifiée de
l'approche physique. La variante remplacée reste dans l'historique à titre de référence.
