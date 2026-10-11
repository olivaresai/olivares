---
title: "Skills — le catalogue immuable et les affectations épinglées"
description: >-
  L'un des 32 modules : le catalogue immuable de skills et ses affectations
  épinglées. Les packs de skills sont installés comme des révisions immuables
  validées par digest ; ce qu'une cible peut utiliser est un pin enregistré et
  audité — jamais une édition en direct d'un dossier.
---

Skills (`modules/skills`, `olivares.skills`) est l'un des 32 modules. Il possède le
**catalogue immuable de skills** et ses **affectations épinglées** : un pack de skills
est installé une fois, validé entrée par entrée contre le digest de son manifeste et
publié comme une révision immuable — il n'est jamais modifié en place ensuite. Ce
qu'une cible (un agent, un groupe, un workspace) peut utiliser est une
**affectation** qui épingle une révision du pack, et chaque pin est écrit dans le
registre d'audit (`skills.assignment.pin`).

## Ce qu'il livre

- **Le catalogue.** Packs installés sous `/v1/m/skills/packs`, chaque révision
  immuable enregistre le digest de son manifeste ; retirer un pack encore affecté ou
  utilisé par des conversations enregistrées est refusé, pas supprimé en cascade et
  en silence.
- **Le catalogue intégré.** Un ensemble vendored de packs de skills amont épinglés par
  commit et digest d'archive (`builtin/PIN.json`), validé à l'installation exactement
  comme une archive téléversée. Rien de tout cela ne s'exécute à l'installation.
- **Les affectations.** `GET/POST /v1/m/skills/assignments` et
  `PUT/DELETE /v1/m/skills/assignments/{id}` épingle une révision sur une cible, dans
  le périmètre du workspace actif. Deux skills affectées avec le même nom et un
  contenu différent sont refusées avant le démarrage d'une session, pour que chaque
  lancement soit déterministe.
- **Import Git.** Un importateur à périmètre opérateur peut récupérer un pack de
  skills depuis une source Git sous une politique épinglée ; l'import atterrit dans le
  catalogue comme une autre révision immuable, pas comme un checkout vivant que le
  moteur lit.

## Surfaces et permissions

Les routes du module vivent dans son espace de noms bêta (`/v1/m/skills/…`),
documentées dans la [référence des routes de modules](/reference/api-beta/) et
rendues dans la vue Skills de la console. Les lectures exigent la permission de
lecture ; installer un pack ou une révision exige l'écriture ; retirer exige admin ;
les affectations exigent la permission d'affectation. Une session résout ses skills
uniquement via des affectations enregistrées — un dossier qui change sur disque ne
change jamais ce qu'un lancement déjà épinglé exécutera.

:::caution[Limites honnêtes]
- **Nouveau depuis la version 26.10.1<!-- release-fixed -->.** Le binaire officiel 26.10.1<!-- release-fixed --> ne contient pas
  ce module ; il arrive avec la prochaine version, et cette page décrit le module tel
  qu'il existe sur la ligne d'intégration actuelle.
- **Le catalogue gouverne ; il n'exécute pas.** Installer, épingler et importer
  n'exécutent jamais de code de skills — l'exécution reste aux outils et sessions qui
  consomment une révision épinglée.
:::

## En relation

- [Catalogue des modules](/fr/reference/modules/overview/) — les 32 modules et où
  celui-ci se situe parmi eux.
- [MCP, skills et capacités](/fr/reference/modules/v-capabilities/) — la vue de
  gouvernance sur les outils et capacités que ce catalogue alimente.
- [Catalogue interne et marketplace](/fr/reference/modules/xiv-catalog/) — le
  marketplace organisé d'agents, serveurs MCP et skills approuvés.
- [Routes de modules (bêta)](/reference/api-beta/) — les opérations
  `/v1/m/skills/`.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — pourquoi l'immuabilité est
  déclarée, pas sous-entendue.
