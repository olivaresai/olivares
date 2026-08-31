---
title: Glossaire
description: >-
  Le vocabulaire du produit, avec précision : l'access map et ses axes
  d'honnêteté, les types d'observation, les primitives de gouvernance et les
  termes opérationnels — chacun défini de la façon dont le moteur l'emploie
  réellement.
---

Les termes sont définis tels que le moteur les emploie — plusieurs sont
délibérément plus étroits que leur usage dans l'industrie, et cette étroitesse est
le but.

### Access map (R/RW map)

Le graphe du module III des **origines** (agents, identités, sessions) et des
**ressources** qu'elles touchent, chaque arête classée par [mode](#mode) et étiquetée
avec sa [signal source](#signal-source), son [attribution](#attribution-confiance)
et son [niveau de couverture](#niveau-de-couverture). Une capacité différenciante clé — l'un
des 30 modules, pas l'ensemble du produit. Voir [Qu'est-ce qu'Olivares AI ?](/fr/start/what-is-olivares-ai/).

### États d'actuation : `v1` / `on-demand` / `seam`

Les trois états honnêtes de la moitié *agissante* de chaque module. **`v1`** — vivant
dans le binaire par défaut sans provisioning. **`on-demand`** — construit et câblé,
mais deny-closed ou dégradé jusqu'à ce qu'un opérateur le provisionne (deploy
apply/retire, déclenchement d'orchestration, dispatch vocal). **`seam`** — une
interface déclarée sans backend. Le [catalogue des modules](/fr/reference/modules/overview/)
marque chaque module ; un garde de régression en CI maintient la table honnête.

### Agent

Un système AI (un agent de codage, un agent de service, une étape de workflow
orchestrée) gouverné comme une entité de première classe, distincte de
l'[identité](#identity--nhi) (credential) sous laquelle il s'exécute. Lier les agents
aux identités est ce qui affine l'[attribution](#attribution-confiance).

### Agent sprawl

Le terme d'analyste pour les agents AI, copilotes et serveurs MCP proliférant à
travers une organisation plus vite que quiconque n'en tient l'inventaire — des agents
inconnus avec des accès inconnus. C'est le problème que l'[access map](#access-map-rrw-map)
et la découverte existent pour rendre visible. Voir
[Vocabulaire d'analyste](/fr/explanation/positioning/analyst-vocabulary/).

### AI TRiSM

*AI Trust, Risk and Security Management* — un framework **inventé et détenu par
Gartner** pour gouverner la confiance, le risque et la sécurité de l'AI. Nous mappons
nos capacités à ses **thèmes** (gouvernance, inspection runtime, enforcement runtime,
gouvernance de l'information) ; nous ne reproduisons **pas** le modèle exact de
Gartner, ne revendiquons aucune conformité, ni n'impliquons d'aval — la taxonomie est
de la recherche propriétaire Gartner. Voir
[Vocabulaire d'analyste](/fr/explanation/positioning/analyst-vocabulary/).

### Approbation (HITL)

Une requête gouvernée pour effectuer une action sous gate, ouverte **deny-closed et
time-boxée**, liée au plan exact, décidée par des humains autorisés avec
séparation-des-tâches et expiration appliquée côté serveur, et consignée dans le
[ledger](#audit-ledger). Voir la [recette](/fr/how-to/cookbook/hitl-approvals/).

### Attribution (confiance)

À quel point un accès observé est fermement lié à une origine *spécifique* :
**`attributed`** (une identité per-agent figure dans la piste) ou **`approximate`**
(inférée — un service account partagé, un store lossy, un processus noyau pas encore
lié à un agent). La map montre le niveau au lieu de fabriquer de la certitude ; la
console rend aussi les arêtes attributed comme *fermes*. Améliorer l'attribution est
un problème d'identité : [SSO/SCIM & sources d'identité](/fr/how-to/connectors/sso-scim-identity/).

### Audit ledger

L'enregistrement append-only, hash-chained, de chaque décision de gouvernance et de
chaque lecture privilégiée, protégé par des signatures Ed25519 — chaque enregistrement
porte `seq`, `prev_hash`, `hash`, `sig`, de sorte que réécrire l'historique est
cryptographiquement détectable. Il ne contient jamais de PII. Exposé sous forme
d'export pull, de sink push et de vérification hors ligne (`olivares audit verify`).

### Break-glass

Une élévation d'urgence gouvernée et auditée pour des actions sous gate *spécifiques* —
délibérément **non** disponible pour tout : réactiver un [kill switch](#kill-switch)
ou finaliser le cycle de vie d'une identité ne peut jamais se faire via break-glass.

### Checkpoint

Une ancre signée sur la chaîne de ledger d'un tenant, écrite à intervalle régulier
(par défaut 1h). Une copie **off-box** du checkpoint et de la clé publique est ce qui
rend la vérification résistante à un attaquant après une compromission de l'hôte.

### Collector

Le processus de bord push-only (`olivares collector`) qui exécute les
[sources](#source) près des systèmes observés et pousse les observations vers le cœur
via gRPC (optionnellement mTLS). Les collectors n'ont **aucun listener entrant**.

### Chemin coopératif

Une observation qui dépend du rapport de l'agent — télémétrie OTLP, hooks. Fidélité la
plus élevée lorsqu'elle est présente, structurellement contournable, et c'est pourquoi
le [backstop kernel](#backstop-kernel) et l'audit store-natif existent à ses côtés.

### Niveau de couverture

La fidélité du signal d'une *ressource*, orthogonale à l'attribution : **clean**
(l'audit natif classe R/W tel quel — pgAudit, CloudTrail), **lossy** (les arêtes
apparaissent mais imprécises), **opaque / impossible passively** (pas de surface
d'audit passive exploitable — le produit le dit au lieu de deviner) ; **mixed**
marque une arête construite à partir de plus d'un niveau.

### Estate de démo

L'estate synthétique que `serve --seed-demo` charge à travers le **vrai** bus
d'événements (loopback-only, mot de passe public de l'arbre source, refuse les binds
non-loopback). Un outil d'apprentissage, jamais un chemin d'installation.

### Destination (connecteur de sortie)

La moitié de livraison du catalogue de connecteurs : Slack, Teams, PagerDuty,
webhook, Splunk HEC, ServiceNow, Jira, email et leurs pairs — ils livrent findings et
notifications, et n'ont pas de niveau de couverture parce qu'ils n'observent rien.

### Bundle DR / KEK

La sauvegarde chiffrée et **sûre pour la continuité du ledger** que produit `olivares
dr backup` ; scellée sous une key-encryption key (dérivée d'une passphrase ou fournie
par un KMS) qui doit voyager séparément des bundles.
Voir [sauvegarde & restauration](/fr/how-to/backup-and-restore/).

### Drift (least-privilege drift)

Le diff entre [Permitted et Observed](#permitted-vs-observed) : l'écart entre l'accès
accordé et l'accès exercé. Trois classes — **unexpected access** (observé, jamais
accordé), **unused grant** (accordé, jamais observé), **reconciliation pending**
(observé, lien d'identité non résolu).
[Recette de triage](/fr/how-to/cookbook/drift-triage/).

### Edge / cost / finding

L'**ensemble fermé** de types d'observation qu'une source peut émettre : une relation
d'accès, un fait de coût d'usage, ou un finding détectif. Fermé par conception — un
connecteur ne peut inventer de nouveaux types, ce qui est ce qui garde le contrat
minimal-data applicable.

### Estate

Tout ce que vous gouvernez dans un déploiement : les agents, identités, serveurs MCP,
modèles, ressources et leurs relations, à travers toutes vos organisations.

### Finding

Une observation guardrail / posture / red-team / forensic, portant un hash de tout
détail sensible plutôt que le détail. Routée sur le rail de notification et vers les
[sinks SIEM](/fr/how-to/cookbook/push-to-siem/).

### Guardian agent

Le terme de **Gartner** pour une AI qui surveille ou intervient sur *d'autres* agents
AI. Olivares AI délivre le **résultat de gouvernance** de la catégorie — observer,
differ permitted-vs-observed, gater deny-closed, enregistrer de façon immuable — mais
en tant que **control plane read-first en dehors du data path**, pas un LLM inline
montant la garde. Voir [Vocabulaire d'analyste](/fr/explanation/positioning/analyst-vocabulary/) ;
à contraster avec la [boucle de gardien](#guardian-loop) intégrée au produit.

### Guardian loop

Une règle de gouvernance qui surveille les findings et engage automatiquement le
containment — y compris le [kill switch](#kill-switch) — l'auto-chemin passant par
exactement le même gate qu'un arrêt humain.

### Identity / NHI

Un principal porteur de credential : humain, ou **non-human identity** (service
accounts, workload identities, clés d'API, identités d'agent). Les rosters arrivent
des [sources d'identité](/fr/how-to/connectors/sso-scim-identity/) ; les lier aux agents
est le pont de l'observation à la gouvernance.

### Backstop kernel

Le chemin d'observation non coopératif : Tetragon capture les events noyau
fichier/réseau hors du contrôle de l'agent ; la source `ebpf` consomme son export.
Toujours [`approximate`](#attribution-confiance) jusqu'à ce qu'une identité lie le
processus à un agent. Voir [eBPF/Tetragon](/fr/how-to/connectors/ebpf-tetragon/).

### Kill switch

L'arrêt d'urgence de l'estate (ou per-agent) : un seul appel admin-tier tue chaque
actuation gouvernée, fail-closed ; la réactivation requiert deux humains distincts
plus une post-revue, sans break-glass autour.
[Recette d'exercice](/fr/how-to/cookbook/kill-switch-drill/).

### Annotation MCP

Un `readOnlyHint` / `destructiveHint` auto-déclaré par un serveur — **non fiable selon
la spécification MCP**, ingéré uniquement comme un hint de capacité déclarée
(`approximate`, ni observé ni permis), corroboré et jamais accordé seul. Voir
[Gouvernance MCP](/fr/how-to/connectors/mcp-governance/).

### Minimal data

La propriété au niveau wire selon laquelle les observations portent des identifiants
et des classifications, jamais des payloads, des corps SQL, des prompts, des secrets
ou des PII. Une propriété du vocabulaire de connecteur, pas un réglage.

### Mode

La classification lecture/écriture d'une arête : `read`, `write`, `readwrite`, ou
`unknown` — prise telle quelle depuis le signal et **jamais inférée** ; `unknown` est
une réponse honnête, pas une réponse manquante.

### Observed / Permitted

Voir [Permitted vs Observed](#permitted-vs-observed).

### Tokens opaques

Les credentials du produit : des tokens aléatoires, révocables, validés côté serveur
(`olvs_…` sessions, `olvk_…` clés d'API, `olst_…` le token de setup à usage unique) —
délibérément pas des JWT, de sorte que la possession d'une clé de signature ne peut
jamais émettre d'accès.

### Organisation (tenant)

La frontière d'isolation. Chaque lecture et écriture de module est tenant-scopée ; sur
Postgres, la row-level security la backstoppe (le moteur refuse de s'exécuter sous un
rôle qui pourrait contourner la RLS).

### Permitted vs Observed

Les deux moitiés que l'access map diffe : les arêtes **permitted** proviennent des
grants déclarés et de la politique ; les arêtes **observed** de la télémétrie et de
l'audit natif. Le diff est le [drift](#drift-least-privilege-drift).

### Admission scellée

Le gate de confiance deny-closed pour les plugins de connecteur hors processus : digest
épinglé + attestation Sigstore vérifiée contre des trust anchors épinglés par
l'opérateur, sans porte de sortie. Voir [construire un connecteur](/fr/how-to/build-a-connector/).

### Setup token

Le token `olst_…` à usage unique imprimé sur stdout au premier boot — toute
l'histoire de la credential de bootstrap ; il n'y a aucune credential par défaut.
Seul son hash est stocké.

### Signal source

Quel observateur a produit une arête : `pg_audit`, `cloudtrail`, `otel`, `ebpf`,
`mcp_annotation`, un grant de politique déclaré, un signal A2A. La provenance n'est
jamais aplatie : un READ pgAudit et un hint MCP ne sont pas la même preuve.

### Sink

Un abonnement d'eventing qui livre des événements à un SIEM dans son dialecte (Splunk
HEC, Sentinel DCR, Datadog, New Relic, ou un webhook générique signé HMAC), en
OCSF/CEF/LEEF/syslog/OTLP/JSON.
Voir [push vers SIEM](/fr/how-to/cookbook/push-to-siem/).

### SLI / SLO

Les niveaux de service publiés : disponibilité via `/readyz`, succès des requêtes,
latence p99 de l'API et de l'ingest — avec les tiers single-node et HA énoncés
séparément et honnêtement.
Voir [monitoring](/fr/how-to/monitor-with-prometheus/).

### Source

Un connecteur d'observation : il fait `Open` avec une config, `Gather` des
observations dans le sink du moteur, et `Close`. Ordonnancement détenu par le moteur,
vocabulaire minimal-data, Apache-2.0, n'importe jamais le cœur.
Voir [connecter une source](/fr/how-to/connect-a-source/).

### Stop gate

Le contrôle d'enforcement que chaque actuation gouvernée effectue contre l'état du
[kill switch](#kill-switch) — vérifié avant tout autre gate, échouant **fermé**
(l'inverse du contrôle de budget, qui échoue ouvert : un compteur cassé ne doit pas
provoquer de panne, mais un contrôle d'arrêt cassé doit).
