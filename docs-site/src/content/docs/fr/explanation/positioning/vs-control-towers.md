---
title: Olivares AI face aux tours de contrôle de l'IA
description: >-
  Comment Olivares AI se positionne par rapport aux tours de contrôle de l'IA et aux
  tableaux de bord de gouvernance d'écosystème (ServiceNow AI Control Tower, plans
  d'administration d'agents des hyperscalers). Nous intégrons, nous ne concurrençons
  pas — nous sommes la source de vérité sous la tour.
sidebar:
  order: 4
---

Une **tour de contrôle de l'IA** est la couche de tableau de bord et de workflow à
l'échelle de l'organisation pour la gouvernance de l'IA : un endroit unique pour voir
les agents enregistrés, router les approbations, ouvrir des tickets et rendre compte de
la posture à la direction. Les exemples incluent **ServiceNow AI Control Tower** et les
plans d'administration d'agents des hyperscalers (les surfaces Entra Agent ID / Agent
365 de Microsoft, les fonctionnalités de gouvernance d'AWS AgentCore).

Si vous avez investi dans l'une d'elles, la bonne question n'est pas « tour ou
Olivares ? ». C'est « qu'est-ce qui alimente la tour en vérité ? ». Notre réponse,
délibérément, est **nous intégrons ; nous ne concurrençons pas.**

:::tip[La version courte]
Les tours de contrôle sont solides sur le **workflow, la billetterie, les tableaux de
bord à l'échelle de l'organisation et la gouvernance des agents au sein de leur propre
écosystème**. Elles sont faibles sur les **parcs hétérogènes, auto-hébergés et
multi-cloud** et sur la **vérité terrain** — ce qu'un agent a réellement touché,
corroboré par rapport au plan de données. Olivares AI est la **couche source sous la
tour** : elle produit l'inventaire attribué, l'écart Permis-vs-Observé et les preuves
à altération détectable, et **les remonte**.
:::

## Ce que les tours de contrôle font bien

- **Workflow et ITSM** : approbations, enregistrements de changements, tickets
  d'incidents, propriété — le processus existant de l'organisation, là où la
  gouvernance de l'IA devrait se brancher plutôt que de créer un silo parallèle.
- **Reporting exécutif** : un seul écran pour la direction couvrant de nombreuses
  initiatives IA.
- **Gouvernance native de l'écosystème** : la tour d'un hyperscaler gouverne bien les
  agents *dans le cloud de cet hyperscaler* — ses identités, ses politiques, son
  runtime.

Ce sont de réelles forces et nous ne les reproduisons pas. Olivares AI n'est pas un
produit ITSM et ne cherche pas à être le tableau de bord de reporting de votre CISO.

## Là où les tours laissent un vide

| Vide | Pourquoi cela compte | Ce qu'Olivares AI fournit |
|---|---|---|
| **Parc hétérogène** | Les agents s'exécutent à travers les clouds, on-prem, sur des portables et en CI — pas seulement dans le runtime d'un seul fournisseur | Inventaire à l'échelle du parc et carte d'accès couvrant les stockages SQL/objet/entrepôt, MCP, outils et l'agent de développement local |
| **Vérité terrain** | Une tour montre ce qui est *enregistré* ; elle corrobore rarement ce que les agents ont *fait* | Télémétrie auto-déclarée recoupée avec pgAudit / CloudTrail / eBPF — Permis-vs-Observé comme un fait |
| **Application sur l'agent de développement** | Les tours observent ; peu peuvent arrêter l'action d'un agent local en mode refus par défaut | Le [PEP via hooks Claude Code](/fr/how-to/connectors/claude-code-hooks-pep/) et les gates d'actuation en refus par défaut |
| **Preuves à altération détectable** | Les tableaux de bord sont mutables ; les auditeurs veulent une preuve immuable | Journal en ajout seul, signé Ed25519 ; packages de preuves OSCAL ; vérification hors machine |
| **Souveraineté** | Les tours SaaS traitent vos données de gouvernance dans leur cloud | Auto-hébergé / air-gapped ; le plan de données ne quitte jamais votre périmètre |

## Comment nous nous branchons (dans les deux sens)

Olivares AI est conçu pour se placer **sous** votre tour et l'alimenter, et pour **lire
depuis** les tours qui exposent un registre.

- **Remonter posture et preuves.** Exportez l'inventaire et la posture pour qu'une tour
  de contrôle les consomme (`GET /v1/m/posture/export`), et transférez le journal
  d'audit et les findings vers votre **SIEM/ITSM** afin qu'ils atterrissent dans le
  workflow que vous exploitez déjà.
  → [Transférer l'audit vers Splunk](/fr/how-to/forward-audit-to-splunk/)
- **Lire les registres d'identités, en lecture seule.** Les connecteurs de fédération
  d'identités synchronisent les registres d'agents depuis **Microsoft Entra Agent ID**,
  **AWS AgentCore Identity**, **Google Agent Identity**, et en lecture seule depuis
  **Microsoft Agent 365** et **ServiceNow AI Control Tower** — en les mappant sur le
  registre SPIFFE/WIF afin que la carte d'accès attribue les arêtes à des identités
  réelles et gouvernées. Voir
  [Où Olivares AI s'intègre avec votre IdP](/fr/explanation/architecture/where-it-fits-with-your-idp/).

La relation est **complémentaire par conception** : la tour possède le workflow et la
vue du conseil d'administration ; Olivares AI possède la vérité terrain et les preuves
immuables qui rendent les chiffres de la tour dignes de confiance.

## Quand la tour suffit

Si l'ensemble de votre parc d'agents vit à l'intérieur d'**un seul** écosystème
hyperscaler ou SaaS, que la tour native de ce fournisseur le gouverne, et que vous
n'avez **aucune exigence de souveraineté ni empreinte hétérogène/auto-hébergée**, vous
n'avez peut-être pas besoin d'un control plane séparé — la tour native plus son export
d'audit peut suffire. Olivares AI devient nécessaire lorsque le parc est **mixte**,
lorsque vous avez besoin d'une **vérité terrain corroborée plutôt que d'un registre**,
ou lorsqu'**un control plane hébergé par un fournisseur n'est pas envisageable pour vos
preuves de gouvernance**.
