---
title: "Exécuter Claude Code avec Olivares (co-déploiement)"
description: "Co-déployez le control plane Olivares et un runtime Claude Code sur une seule machine Linux, sécurisé par défaut, afin que le moteur lance, gouverne et démantèle des sessions Claude Code partageant un workspace — en quatre topologies."
---

C'est la moitié **Operate** de l'histoire Anthropic-first : non seulement *observer* et
*gouverner* Claude Code, mais le **conduire**. Le control plane lance un véritable processus `claude`,
relie ses E/S dans un flux gouverné, ancre chaque transition de cycle de vie dans
le ledger d'audit, et le démantèle — sur un workspace partagé, depuis l'API/la CLI (et,
plus tard, le portail), **sans SSH**. Cette page co-déploie les deux moitiés sur un seul hôte Linux
en quatre topologies, sécurisées par défaut.

Pour le chemin *observation coopérative* (télémétrie OTLP → access map), voir
[Connecter Claude Code](/how-to/connect-claude-code/) ; pour le chemin *gouvernance* (hooks PreToolUse
en tant que PEP), voir l'[exemple govern-claude-code](https://github.com/olivaresai/olivares/tree/main/examples/govern-claude-code).
Cette page traite du **co-déploiement** : faire fonctionner les deux runtimes ensemble.

:::note[Comment la gouvernance atteint réellement la session]
Une session est gouvernée parce que **le moteur possède les stdin/stdout de `claude`** — le
transport headless `stream-json`. Le moteur lance `claude` en tant que processus enfant (le
procRunner natif) et relie chaque frame NDJSON. Cela ne fonctionne que lorsque le moteur et
`claude` partagent un contexte d'exécution (le même hôte, ou le même conteneur). Les topologies
recommandées les placent ensemble pour exactement cette raison ; les topologies mixtes,
et leurs contraintes honnêtes, sont décrites plus bas.
:::

## Deux principes avant de commencer

1. **Opt-in.** L'image de base Olivares est distroless et **ne porte aucun `claude`**. La
   couche Operate-Claude-Code est un artefact *distinct* — une image combinée
   (`Dockerfile.agentops`) ou un module complémentaire d'installation native. Si vous n'exécutez pas de
   Claude Code gouverné, vous ne la récupérez jamais, et sa surface supplémentaire ne touche jamais votre
   control plane.
2. **Source officielle, jamais redistribuée.** Les conditions d'Anthropic n'autorisent pas la
   redistribution du binaire `claude`, donc nous **l'installons depuis la source officielle d'Anthropic,
   signée GPG** au build/premier lancement (les dépôts apt/dnf/apk signés), épinglée
   et avec l'auto-updater désactivé. Nous ne livrons aucun binaire tiers. Vous pouvez aussi
   **apporter le vôtre** (`claude`) et y pointer le moteur.

## Les quatre topologies en un coup d'œil

| # | Olivares | Claude Code | Comment le moteur le conduit | Statut |
|---|----------|-------------|----------------------------|--------|
| 1 | Docker | Docker | **Même conteneur** (image combinée), enfant procRunner | **Recommandé** (même chemin gouverné que 2) |
| 2 | Natif | Natif | Même hôte (systemd), enfant procRunner | **Recommandé**, testé en smoke de bout en bout |
| 3 | Docker | Natif (hôte) | Inter-namespace — non gouvernable tel quel | Co-localisez plutôt (voir ci-dessous) |
| 4 | Natif | Docker (par session) | Conteneur par session via l'API Docker | À venir (documenté) |

Les deux topologies **co-localisées** (1, 2) sont le défaut sécurisé. La topologie 2 (native) est
testée de bout en bout par [`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh) ;
la topologie 1 réutilise le **même** chemin procRunner gouverné (le build/run de l'image combinée
n'est pas encore câblé dans un test automatisé). Les topologies 3 et 4 veulent le gouverneur et le gouverné dans des
conteneurs *différents* ; relier le stdio à travers cette frontière nécessite un accès à l'API Docker (un
privilège que le moteur ne prend délibérément **pas** par défaut). Leurs chemins honnêtes sont
détaillés dans [Topologies mixtes](#topologies-mixtes-3-et-4).

---

## Topologie 1 — les deux dans Docker (recommandée)

Un conteneur durci exécute le moteur **et** `claude` ; un volume workspace est le
répertoire de travail partagé. Loopback uniquement, non-root, système de fichiers racine en lecture seule —
posture identique au compose de base, plus le runtime conduit.

### Construire l'image combinée

`claude` est installé au moment du build depuis le **dépôt apt signé** d'Anthropic, avec
l'empreinte de la clé de signature épinglée (`31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE`) et
l'auto-update désactivé. Épinglez la base du moteur par digest et vérifiez-la d'abord :

```sh
# verify the engine image you build FROM (it is cosign-signed)
cosign verify docker.io/olivaresai/olivares:26.8.0 \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

docker build -f Dockerfile.agentops \
  --build-arg OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest> \
  --build-arg CLAUDE_CHANNEL=stable \
  -t olivares-agentops:26.8.0 .
```

Apportez plutôt votre propre `claude` avec `--build-arg CLAUDE_INSTALL=byo` (l'image est livrée
sans `claude` ; montez le vôtre au runtime et définissez `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`).

### Démarrer

```sh
export OLIVARES_AGENTOPS_IMAGE=olivares-agentops:26.8.0
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.agentops.yml up -d
```

L'override ne change que ce dont Operate a besoin : l'image combinée, quatre volumes inscriptibles
(données du moteur, **workspace**, home `~/.claude` de claude, le jeton d'inférence à durée de vie courte),
et l'environnement du session-runtime. Tout le reste — les ports liés à `127.0.0.1`, uid 65532,
racine `read_only`, `cap_drop: ALL`, `no-new-privileges` — est hérité de la base.

:::caution[La première session gouvernée a besoin d'un credential d'inférence]
La source du credential est **deny-closed** : un lancement `stream-json` lit un jeton bearer
*à durée de vie courte* depuis `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` (`/run/olivares/session-token`,
sur le volume `olivares-runtime`) et le jette — seul un `credential_id` non sensible
est jamais stocké. Pointez votre rafraîchisseur WIF/SPIFFE/OIDC vers ce volume. Tant qu'un jeton n'est
pas présent, les lancements `stream-json` échouent **closed** — le moteur s'exécute toujours et est par
ailleurs gouvernable ; câbler l'auth est votre étape délibérée. (L'échange de jeton in-process en direct est
câblé séparément.)
:::

---

## Topologie 2 — les deux en natif (sans Docker)

Le moteur et `claude` sur l'hôte ; systemd exécute le moteur, qui conduit `claude`. Le
workspace réside dans `/var/lib/olivares/workspaces`.

### Une seule commande

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install-agentops.sh | sh
```

Il détecte automatiquement la topologie native, installe le binaire du moteur **vérifié** (le
`install.sh` gardé par cosign), installe `claude` depuis le dépôt apt/dnf/apk signé (avec
vérification de l'empreinte de clé — ou `OLIVARES_CLAUDE_INSTALL=byo` pour ignorer), crée
l'utilisateur de service `olivares` sans login et le répertoire workspace, et dépose
l'override systemd durci + l'exemple d'environnement. Il ne démarre **pas** automatiquement un plan de
gouvernance — en exécuter un est votre décision explicite.

### Ce que l'installateur câble (et pourquoi)

- `packaging/systemd/olivares.service.d/agentops.conf` — un drop-in qui donne au
  `claude` conduit un `HOME` inscriptible pour `~/.claude` (gardé sous `/var/lib/olivares`,
  de sorte que `ProtectHome=true` protège toujours les véritables utilisateurs), s'assure que le répertoire
  workspace existe, et lève exactement **une** propriété de sandbox : `MemoryDenyWriteExecute` (le runtime
  `claude` compile en JIT et a besoin de mémoire W→X). Toutes les autres directives de durcissement de
  l'unité de base restent en vigueur.
- `/etc/olivares/agentops.env` — la configuration du session-runtime (fichier de jeton, TTL, URL de
  base de gateway optionnelle, chemin BYO `claude` optionnel).

Ensuite, délibérément :

```sh
sudo nano /etc/olivares/agentops.env     # wire the short-lived inference token (refresher)
sudo systemctl enable --now olivares     # loopback-only by default
```

:::note[Pourquoi il n'y a pas de service `claude` séparé]
Un démon `claude` long-running mettrait ses stdin/stdout hors de portée du moteur — et
le transport gouverné *est* le stdio. Donc le moteur lance et possède le processus `claude`
lui-même ; l'« unité runtime » est le propre service du moteur, configuré pour le rôle Operate
par le drop-in.
:::

---

## Lancer la première session gouvernée

Les mêmes étapes dans l'une ou l'autre topologie co-localisée. Authentifiez la CLI, enregistrez le
workspace partagé, lancez :

```sh
export OLIVARES_SERVER_URL=https://127.0.0.1:8443
export OLIVARES_TOKEN=<your-api-token>
export OLIVARES_TENANT=<your-tenant-id>

# 1) register the shared workspace (the session's working dir; jailed file API on top)
olivares agent workspace add /var/lib/olivares/workspaces/project-x --name project-x --mode rw

# 2) launch a governed session over the stream-json transport
olivares agent session create --transport stream-json \
  --permission-mode acceptEdits --model opus \
  --workspace <workspace-ref> --isolation native

# 3) attach to its live, bridged I/O (lossless replay from a cursor); send input; stop
olivares agent session attach <run-ref>
olivares agent session input  <run-ref> --line '{"type":"user","message":{"role":"user","content":"…"}}'
olivares agent session stop   <run-ref>
```

Chaque transition (`created → launched → … → stopped`) est **ancrée dans le ledger d'audit
signé** (`olivares agent session events <run-ref>`) ; l'API de fichiers du workspace
(`olivares agent workspace files|get|put|…`) est jailée et auditée. Le contrat de reproductibilité
de tout cela est [`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh),
qui fait monter le co-déploiement natif contre un faux `claude` hermétique et affirme que la
session est gouvernable de bout en bout.

:::note[Seul `--isolation native` est fonctionnel dans cette version]
`--isolation container` et `--isolation sandbox` sont des **valeurs de seam de
compatibilité ascendante, pas encore câblées** (le Runner de conteneur par session est le suivi documenté dans
[Topologie 4](#topologie-4--olivares-natif-claude-dans-un-conteneur-par-session)). Le runner
natif **refuse** un lancement container/sandbox (une erreur claire) plutôt que d'exécuter silencieusement
`claude` sans l'isolation que vous avez demandée. Utilisez `native` — sous l'image combinée /
le co-déploiement systemd, c'est la propre frontière conteneur/hôte durcie du moteur.
:::

:::caution[`bypassPermissions` doit rester derrière la gouvernance]
Exécuter `claude` en headless avec un `--permission-mode` permissif (`bypassPermissions`,
`dontAsk`) est exactement le moment où vous voulez le plan de gouvernance. L'environnement à allowlist du
moteur ne fuit jamais un secret `OLIVARES_*`/`ANTHROPIC_*` vers l'agent, et le
PEP PreToolUse / le budget / le kill-switch décident de ce que la session peut réellement faire.
:::

---

## Topologies mixtes (3 et 4)

Celles-ci séparent le gouverneur et le gouverné de part et d'autre d'une frontière de conteneur. Soyez
lucide sur ce que cela coûte.

### Topologie 3 — Olivares dans Docker, Claude sur l'hôte

Il n'y a **aucun chemin gouverné propre** : un moteur conteneurisé ne peut pas posséder le stdio d'un
processus dans les namespaces de l'hôte, et le transport gouverné est le stdio. Atteindre un `claude`
hôte exigerait de partager le namespace PID de l'hôte et les mounts dans le conteneur du moteur
— une dé-isolation importante et délibérée qui anéantit l'intérêt de contenir le
moteur. **Co-localisez plutôt** : exécutez les deux dans l'image combinée (c'est *bien* la topologie 1), ou
exécutez les deux en natif (topologie 2). C'est une limite réelle, énoncée plutôt que masquée.

### Topologie 4 — Olivares natif, Claude dans un conteneur par session

C'est le foyer naturel de l'**isolation par conteneur frais par session** : chaque session reçoit
un tout nouveau conteneur `claude` durci (workspace bind-mounté, racine en lecture seule, non-root,
cap-drop), créé et démantelé par le moteur via l'API Docker, avec stdio relié
par Docker attach/hijack. Le seam du modèle de données le **modélise** déjà (`--isolation container`
est une valeur valide, et la primitive de mount de l'exécuteur qu'il consommera est déjà livrée) — mais le
runner qui le porte n'est pas encore câblé, donc le runner natif refuse cette valeur aujourd'hui (voir
la note ci-dessus).

**C'est un suivi documenté, non livré dans cette version.** Piloter des conteneurs frères
signifie donner au moteur l'accès à l'API Docker (idéalement via un proxy de socket au moindre
privilège) — une surface de confiance que cette version évite délibérément au profit de l'image
combinée sans socket. Choisir cette topologie, c'est choisir une isolation gouverneur/gouverné plus forte
*au prix de* cet octroi d'API Docker ; elle arrivera derrière le seam `isolation=container` existant.
D'ici là, le défaut sécurisé est la co-localisation.

---

## Posture de sécurité (toutes topologies)

- **Loopback par défaut.** Les ports de l'hôte ne publient que sur `127.0.0.1`. Dans un conteneur, le
  moteur écoute sur `0.0.0.0` *à l'intérieur* du conteneur, donc le **mapping du port de l'hôte est la
  frontière d'exposition** — ne le publiez jamais sur une adresse d'hôte non-loopback sans votre propre
  proxy d'auth qui termine le TLS. Le bind natif/systemd par défaut est loopback. Exposez délibérément.
- **Non-root, moindre privilège.** uid/gid 65532, système de fichiers racine en lecture seule, `cap_drop:
  ALL`, `no-new-privileges` (Docker) / l'ensemble complet `Protect*`/`Restrict*` moins l'unique
  relâchement W^X documenté (systemd).
- **Environnement à données minimales, à allowlist.** Le `claude` enfant n'hérite que d'une allowlist
  explicite (PATH, HOME, locale…) plus le jeton d'inférence en mémoire — **aucune** clé de signature
  `OLIVARES_*`, **aucun** `ANTHROPIC_*`/`CLAUDE_CODE_*` ambiant qui pourrait masquer le
  credential émis.
- **Chaîne d'approvisionnement vérifiée.** Le moteur est signé par cosign (vérifiez-le / épinglez par
  digest) ; `claude` s'installe depuis les dépôts signés d'Anthropic avec l'empreinte de clé épinglée.
  L'installateur **refuse d'exécuter un moteur non vérifié** sauf si vous le désactivez explicitement.
- **Audit ancré.** Chaque transition de cycle de vie et chaque mutation de workspace est scellée dans
  le ledger signé et hash-chaîné par `PayloadHash` — les octets des fichiers et le contenu
  des frames ne sont jamais persistés.

## Voir aussi

- [Connecter Claude Code](/how-to/connect-claude-code/) — le chemin d'observation coopérative.
- [Sécurité et durcissement](/how-to/security-hardening/) — la posture de référence du moteur.
- [Vérifier une version](/how-to/verify-a-release/) — vérification cosign / SBOM / SLSA.
- [INSTALL.md](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#operate-claude-code-co-deployment) — la matrice d'installation, y compris ce co-déploiement.
