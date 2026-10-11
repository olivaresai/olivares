---
title: "Reporting — rapports HTML/PDF professionnels"
description: >-
  Génère des rapports HTML et PDF téléchargeables à partir des données de
  conformité, d'audit et FinOps de la plateforme. Cinq types intégrés sont
  disponibles à la demande ; les rapports planifiés font partie de Business.
---

Reporting (`modules/reporting`) est **LIVE**. Il transforme les données de
conformité, d'audit et FinOps de la plateforme en un document professionnel
unique, afin qu'un auditeur télécharge les preuves au lieu de copier-coller du
JSON provenant de plusieurs API.

**Édition :** Business Compliance Packs propose les catalogues de référentiels, les évaluations, le calendrier réglementaire, les vues DORA/HIPAA, le scellement des preuves, les exports OSCAL et les rapports HTML/PDF à la demande. Community renvoie `501` pour ces fonctions et conserve le risque, la résidence, la gestion des enregistrements et les exports JSON/CSV des preuves enregistrées. Les mises à jour préservent les enregistrements existants.

## Rapports intégrés

Business Compliance Packs fournit cinq types de rapports à la demande :

- `compliance-evidence` — posture de conformité par référentiel, avec statut des contrôles et preuves.
- `audit-summary` — synthèse des événements d'audit et vérification de l'intégrité du ledger.
- `finops-report` — dépenses d'IA par modèle et fournisseur.
- `access-review` — utilisateurs et données d'accès pour les revues périodiques.
- `executive-summary` — vue compacte de la gouvernance, des risques, des coûts et de l'adoption.

`GET /v1/m/reporting/reports` liste les types et formats. Un rapport se génère
avec `GET /v1/m/reporting/reports/{type}` ; HTML est le format par défaut et
`?format=pdf` télécharge un PDF. Les routes exigent `reporting:report:read`.

## Limites, clairement énoncées

- La génération PDF lance Chromium en mode headless. Sans `chromium`,
  `chromium-browser` ou `google-chrome`/`chrome` dans le `PATH`, les requêtes PDF
  renvoient `501` ; HTML reste disponible.
- Un rapport compliance-evidence nécessite la source de données de conformité.
  Si elle n'est pas câblée, le document affiche explicitement « Data source not
  configured » au lieu d'inventer des preuves.
- Ce module rend des documents à partir de données déjà détenues par la
  plateforme. Il ne remplace ni le ledger d'audit, ni l'évaluation de conformité,
  ni la source de vérité FinOps.

## Voir aussi

- [Conformité et réglementation](/fr/reference/modules/xiii-compliance/) — la
  source de posture et de preuves de conformité.
- [Coûts et AI FinOps](/fr/reference/modules/xi-finops/) — la surface de dépenses
  faisant autorité.
- [Catalogue des modules](/fr/reference/modules/overview/) — les 32 modules
  câblés et leur maturité déclarée avec honnêteté.
