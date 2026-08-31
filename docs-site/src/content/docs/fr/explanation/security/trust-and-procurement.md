---
title: Confiance et approvisionnement
description: >-
  Ce que l'équipe sécurité d'un acheteur peut vérifier aujourd'hui : la
  préparation à la certification (et non des affirmations), le programme de
  tests d'intrusion, le modèle d'objectifs de réponse du support, la conformité en matière
  d'accessibilité et les preuves de conformité lisibles par machine — ainsi que
  ce qui, en toute honnêteté, n'existe pas encore.
---

Cette page est le point d'entrée pour les équipes sécurité, conformité et
approvisionnement qui évaluent Olivares AI. La posture de conformité du produit
suit une seule règle, appliquée dans le code autant que dans la prose :
**énoncer ce qui est construit et vérifiable ; ne jamais revendiquer une
attestation qui n'existe pas.** Le module de conformité signale un contrôle
étayé uniquement par des preuves de conception comme `by_design` — jamais
`satisfied` — et chaque entrée de framework dans le catalogue porte sa propre
clause de non-responsabilité « pas une certification ».

:::note[État actuel, sans surprises]
Olivares AI ne détient **aucun rapport SOC 2, aucun certificat ISO/IEC 27001 ou
42001**, n'a **pas encore fait l'objet d'un test d'intrusion par un tiers**, et
**ne figure pas** dans le CSA STAR Registry. Ce qui existe à la place — et qui
est sans doute plus utile avant la signature d'un contrat — est un package de
préparation vérifiable : des correspondances contrôle par contrôle vers des
preuves que vous pouvez extraire vous-même d'un déploiement en fonctionnement,
ainsi que la liste explicite des décisions (engagements de certification,
contractualisation des tests d'intrusion, activation du support commercial) qui
restent ouvertes. FedRAMP/ATO est explicitement hors périmètre pour le produit
auto-hébergé.
:::

## Le package de confiance

Le package complet destiné aux acheteurs réside dans le dépôt sous
`docs/trust/` :

- **Préparation à la certification** — des correspondances SOC 2 Type II,
  ISO/IEC 27001:2022 et ISO/IEC 42001:2023 de chaque contrôle vers la capacité
  du produit et le point de terminaison de preuve en direct qui l'étaye, y
  compris les preuves spécifiques à l'IA qu'un auditeur de 2026 demande
  (journalisation des prompts/interactions, versionnage des modèles, lignage,
  inventaire des sous-traitants LLM).
- **Banque de réponses aux questionnaires** — des réponses fournisseur
  pré-vérifiées alignées sur les domaines Shared Assessments SIG 2026 et prêtes
  à transcrire dans un CSA AI-CAIQ pour STAR for AI Level 1.
- **Programme de tests d'intrusion** — cadence engagée (test par un tiers avec
  périmètre défini lors de la première GA commerciale, annuel par la suite,
  re-tests déclenchés par événement), périmètre, et un flux de remédiation câblé
  aux objectifs de remédiation de CVE publiés dans `SECURITY.md`.
- **Architecture de référence** — topologies de déploiement (nœud unique, HA
  actif-passif, multi-région, air-gapped), zones de confiance, lignes de base de
  dimensionnement mesurées, paliers RPO/RTO, et la surface d'intégration
  IdP/SIEM/ITSM/KMS.
- **Artefacts d'approvisionnement UE** — un modèle de documentation technique
  Annexe IV de l'EU AI Act rempli à partir de preuves en direct, et une
  table de correspondance clause par clause vers les clauses contractuelles
  types MCC-AI de la Commission (variantes High-Risk et Light).
- **Dossier de sûreté des agents** — un modèle d'argumentation structurée de
  type CAE, prospectif, avec des colonnes honnêtes de risque résiduel.
- **Risque mono-fournisseur** — l'objection de viabilité traitée
  structurellement : le cœur AGPL est la plateforme de gouvernance complète, sans
  aucune fonctionnalité bridée en interne pour pousser à la vente (une petite
  gamme commerciale additive est construite séparément et distribuée en privé,
  absente du binaire ouvert — elle ajoute des capacités par-dessus, sans jamais
  en retirer au cœur ouvert) ; dans ce binaire ouvert, la clé de licence sert
  uniquement à l'attestation et fonctionne hors ligne — elle n'active rien —, et
  les builds sont reproductibles et assortis d'une attestation de provenance, de
  sorte que la continuité ne dépend pas de l'existence du fournisseur.

## Ce que vous pouvez vérifier sans nous faire confiance

L'auto-hébergement inverse la relation d'attestation habituelle : la plupart des
contrôles qu'un rapport SOC 2 attesterait, vous pouvez les vérifier directement
dans votre propre déploiement.

- **Releases :** signatures cosign, SBOM, provenance SLSA Build L3 (SLSA v1.2), OpenVEX — voir
  [Vérifier une release](/fr/how-to/verify-a-release/).
- **Contact de sécurité et divulgation :** le canal de signalement, le délai de
  divulgation coordonnée et les objectifs de remédiation de CVE sont publiés dans `SECURITY.md` et
  annoncés de façon lisible par machine sur [`/.well-known/security.txt`](https://olivares.ai/.well-known/security.txt)
  (RFC 9116), de sorte qu'un scanner ou un chercheur trouve le canal sans avoir à demander.
- **Altérations détectables :** l'audit ledger en ajout seul, chaîné par hachage et signé
  par événement se vérifie hors ligne — voir le
  [modèle de sécurité](/fr/explanation/security/security-model/).
- **Preuves de conformité en direct :** statut des frameworks, analyse des
  écarts, packages de preuves scellés (JSON/CSV/OSCAL), AIBOM des modèles
  (CycloneDX 1.6 / SPDX 3.0.1 AI profile), model cards et le calendrier
  réglementaire sont tous des réponses d'API, pas des PDF — le produit traite
  les dates et les correspondances de conformité comme des données figées par
  version.
- **Affirmations opérationnelles :** les SLO, le dimensionnement et les chiffres
  RPO/RTO de l'architecture de référence renvoient à des lignes de base mesurées
  et versionnées dans le dépôt.

## Support et accessibilité

- Le modèle de support (paliers, objectifs de réponse selon la gravité,
  escalade) est publié dans `SUPPORT.md` — y compris la divulgation honnête que
  le support commercial est défini mais pas encore achetable, et que la chaîne
  d'escalade ne compte aujourd'hui qu'une seule personne.
- Le rapport de conformité en matière d'accessibilité est un ACR édition **VPAT
  2.5Rev INT** complété (WCAG 2.1/2.2 AA + Revised Section 508 + EN 301 549
  V3.2.1) dans `docs/accessibility/VPAT-olivares-admin.md`, le passage formel
  avec technologies d'assistance restant en attente et divulgué comme tel. La
  console est livrée en anglais et en espagnol ; la feuille de route i18n au-delà
  de EN/ES est conditionnée à la demande et documentée dans le package de
  confiance.

## Centre de confiance public

Le [Centre de confiance](https://olivares.ai/trust) sur le site web du produit présente les mêmes artefacts de chaîne d'approvisionnement décrits ci-dessus sur une page publique indépendante : attestations SLSA Build L3, signatures cosign, téléchargements SBOM, avis OpenVEX et le script de vérification. Les titulaires de licences commerciales peuvent accéder aux artefacts de conformité par version via le [portail client](https://licenses.olivares.ai/portal).

## Où aller ensuite

- [Modèle de sécurité](/fr/explanation/security/security-model/) — comment la
  plateforme se défend.
- [Modèle de menaces](/fr/explanation/security/threat-model/) — adversaires et
  frontières de confiance.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — ce qui fonctionne
  aujourd'hui par rapport à ce qui est prévu, à l'échelle du produit.
- [Centre de confiance](https://olivares.ai/trust) — vérification publique de la chaîne d'approvisionnement et statut de conformité.
