---
title: "Enregistrement de sessions privilégiées"
description: >-
  Un enregistrement immuable, ancré au journal et rejouable de ce qu'une session
  d'opérateur privilégié a réellement fait sur les surfaces les plus sensibles du
  moteur : trames en ajout seul, expurgées à l'écriture, chaînées par hachage par
  session et ancrées dans le journal d'audit signé par PayloadHash. Aligné PAM,
  LIVE.
---

L'enregistrement (`modules/recording`) est le plan d'**enregistrement de
sessions privilégiées** — le contrôle aligné PAM que les acheteurs à forte
assurance attendent pour les consoles et l'accès d'urgence. Il capture, sous
forme de preuves structurées, ce qu'une session d'opérateur privilégié a
réellement fait sur les surfaces de module les plus sensibles, et lie ces preuves
au journal d'audit à altération détectable afin qu'elles ne puissent être réécrites après
coup. **Maturité : LIVE.**

## Ce qu'il enregistre

Une **session d'enregistrement** est la fenêtre privilégiée d'un identifiant — la
session de connexion d'un opérateur humain, ou un jeton de service sur le
plancher break-glass — à l'intérieur d'un tenant. Ses **trames** forment une
piste en ajout seul (garanties d'immuabilité au niveau de la base de données),
une trame par action de route de module sur une surface enregistrée : qui, quand,
la forme de la route et la permission, les identifiants de cible expurgés, la
délégation, le résultat, et un SHA-256 à sens unique du corps de la requête. Les
trames sont des **événements d'action structurés, jamais des transcriptions ni
des corps** — les valeurs de paramètres passent par un expurgateur borné à
l'écriture, de sorte qu'une valeur en forme d'e-mail ou d'identifiant ne persiste
jamais.

La capture se situe au niveau du wrapper de route de module du moteur et est
**fermée par défaut (deny-closed)** : sur une surface enregistrée, l'absence de
preuve ajoutable signifie l'absence d'action privilégiée. La portée enregistrée
couvre toute route break-glass pour tout principal (le plancher obligatoire et
non configurable) plus les espaces de noms privilégiés configurés par tenant.

## Intégrité et rejeu

Les trames de chaque session sont **chaînées par hachage**, et l'extrémité de la
chaîne est **ancrée dans le journal d'audit signé** par `PayloadHash` — un
événement d'ouverture au démarrage de la session, des ancrages périodiques
pendant son exécution, et un scellement à sa clôture. Réécrire une trame brise à
la fois la chaîne de session et ses ancrages de journal scellés.
`GET /sessions/{id}/verify` recalcule la chaîne et vérifie chaque ancrage ;
`GET /sessions/{id}/replay` reconstruit la chronologie lisible par un humain,
corrélée avec la fenêtre de journal de la session. La surface s'enracine à
`/v1/m/recording/` (`sessions`, `replay`, `verify`, `seal`, `config`, `ack`).

## Contexte délimité, énoncé clairement

- Il enregistre les **routes de module** (`/v1/m/<ns>`) ; les surfaces de cœur
  `/v1` sont auditées au journal mais pas enregistrées par trame — le rejeu les
  corrèle plutôt à travers la fenêtre de journal de la session.
- Sur une session **active**, les trames postérieures au dernier ancrage
  périodique ne sont liées que par l'extrémité de la chaîne jusqu'au prochain
  ancrage ou scellement ; `verify` rapporte `anchored_through` de sorte que la
  limite soit explicite, jamais implicite.
- Il n'implémente **ni purge ni conservation légale (legal hold)** — la
  rétention/conservation légale possède la suppression ; les ancrages de journal
  survivent à toute purge.
- C'est le sous-système d'enregistrement que le **panneau de gouvernance
  agentops** utilise pour l'enregistrement des E/S par session : chaque trame
  Claude Code pontée est repliée dans le même motif chaîné par hachage et ancré
  au journal.

## Voir aussi

- [Sécurité](/fr/reference/modules/ix-security/) — le plan environnant de
  sécurité et de protection des données (garde-fous, DLP, rétention, résidence).
- [Sessions](/fr/reference/modules/ii-sessions/) — héberge le runtime gouverné de
  session Claude Code dont ce sous-système enregistre les E/S par session.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — la posture live / à la
  demande / fermée par défaut à travers le moteur.
