---
title: "Module XIII — conformité et réglementaire"
description: >-
  Cartographier ce que le control plane observe et audite déjà sur les
  cadres réglementaires, et exporter des preuves consommables par un auditeur dérivées du
  registre en ajout seul. Conçu-pour-l'audit, jamais certifié : statut + preuves, jamais « conforme ».
---

Le module XIII ouvre les portes de l'entreprise en **cartographiant** ce que le control plane observe et
audite déjà sur les cadres réglementaires, et en produisant des **preuves consommables par un
auditeur** dérivées du registre d'audit en ajout seul, à chaînage de hachage (hash-chained). C'est un
module de la couche intelligence : il ne capture **rien de nouveau** — il agrège et transforme
ce que le noyau et les autres modules enregistrent déjà, et il **ne revendique jamais de
certification**.

## Ce que c'est

Le module XIII a cinq surfaces, toutes en lecture-et-dérivation sur des données existantes :

- **Un catalogue de contrôles versionné** conservé dans le dépôt comme source de vérité
  déterministe — EU AI Act, NIST AI RMF, ISO/IEC 42001, SOC 2 / ISO 27001 et RGPD (plus
  des correspondances croisées GenAI/agentiques), modélisés en **contrôles** versionnés, chacun avec son
  exigence et son critère de satisfaction. C'est une **cartographie technique, pas un conseil
  juridique**, et un contrôle dont la plateforme ne peut pas attester l'obligation porte une
  note explicite, de sorte qu'une couverture partielle ne se lise jamais comme totale.
- **Une carte déclarative contrôle → preuve.** Chaque contrôle est mappé sur des **capacités** du
  control plane. Une capacité est soit **opérationnelle** — présente uniquement lorsqu'il existe de vraies
  données de tenant (un registre qui se vérifie, des arêtes d'accès observées, des findings de sécurité,
  des résultats d'évaluation, des déploiements, une classification de risque, une attestation de résidence) — soit
  **architecturale** — une garantie de conception de la plateforme citée aux documents de conception et étiquetée
  comme telle, jamais comme de la télémétrie.
- **Des preuves d'audit exportables** — un paquet de preuves scellé, en ajout seul, dérivé du
  registre.
- **Classification du risque des agents** dans un palier de l'EU AI Act mis en correspondance croisée avec les
  fonctions du NIST AI RMF, à partir d'attributs observés — gouvernée et auditée.
- **Résidence des données** — une attestation par région de l'emplacement où le déploiement et ses
  stores s'exécutent réellement, plus un scan qui transforme les signaux d'egress existants en un
  finding de résidence.

## Statut des contrôles & entités

Le statut est calculé honnêtement, jamais asserté. Un contrôle est **satisfied** uniquement lorsque chaque
capacité mappée est présente **et qu'au moins une est opérationnelle** ; **by_design** lorsque toutes les
capacités présentes sont architecturales (prêtes par conception, jamais satisfaites) ; **partial** lorsque
certaines sont présentes ; **gap** lorsqu'aucune ne l'est ; **unmapped** lorsqu'aucune capacité ne l'étaye du tout.
`satisfied` ne repose jamais sur une preuve de conception seule.

Le module déclare quatre entités en ajout seul / auditées dans le modèle de données partagé : un
**paquet de preuves** scellé (enregistrant le numéro de séquence et le hachage de la tête de chaîne ainsi que le résultat
de vérification live de la chaîne de hachage), un **résultat** par contrôle au sein de ce paquet, une **classification
du risque** par sujet, et une **attestation de résidence** par région. Le paquet de preuves **référence**
le registre par séquence et hachage et rend toute altération de son corps détectable
avec un hachage de manifeste déterministe — il ne copie jamais le registre et
ne contient jamais de payloads ni de PII.

## Ce qu'il consomme & produit

La classification du risque lit des attributs déjà enregistrés par d'autres modules — les
[arêtes d'accès](/fr/reference/modules/iii-access-map/) sortantes en lecture/écriture, les findings de sécurité high/critical,
et un signal d'autonomie optionnel — et produit un palier **suggéré** qui est
gouverné : un humain doit le revoir et l'approuver, et le moteur de suggestion **ne peut jamais
assigner le palier inacceptable** (c'est une détermination juridique). Le scan de résidence
corrèle la lignée d'egress existante avec les attestations `self_hosted` et, par
violation, lève un finding central et publie un signal de bus interne pour que le module de
notifications (XV) le diffuse vers SIEM/Slack/PagerDuty. Lire ou exporter un
paquet de preuves, en sceller un, classifier ou revoir un risque, et attester une résidence
sont des actions privilégiées, à portée de tenant, qui **s'auto-auditent dans le registre** dans la
propre transaction de l'appelant.

:::caution[Limites honnêtes]
- **Conçu-pour-l'audit, jamais certifié.** Chaque réponse de reporting porte la
  clause de non-responsabilité indiquant que ce n'est **pas une certification et pas un conseil juridique**. La sortie parle
  de statut de contrôle et de preuves ; elle ne dit jamais « conforme » ni « certifié ». Les garanties en
  opt-in (telles que le chiffrement au repos) sont par défaut **absentes** jusqu'à attestation.
- **Aucun actionnement.** Ce module cartographie des contrôles et exporte des preuves — il ne
  remédie pas, n'applique pas et ne change rien. Son seul effet de bord est le finding de résidence
  et le signal de bus, sur lesquels d'autres modules agissent.
- **Une preuve ne vaut que ses sources.** Un contrôle sans donnée de tenant qui l'étaye est
  un **gap** honnête, pas une réussite falsifiée ; une capacité opérationnelle absente abaisse le
  statut d'un cadre plutôt que de le gonfler. La preuve de least-privilege drift
  consomme le drift **réconcilié** du module III (et non le chemin store brut), elle hérite donc
  des limites de couverture par paliers du module III — une arête absente n'est pas une preuve qu'un accès n'a pas
  eu lieu.
- **La preuve architecturale est une conception, pas une preuve.** Les capacités citées aux documents de
  conception attestent comment la plateforme est construite, pas qu'un contrôle s'est exécuté dans votre tenant ; elles
  produisent `by_design`, qui est délibérément distinct de `satisfied`.
:::

## Liens connexes

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe le module XIII et la
  séparation honnête gouverner/observer-vs-actionner.
- [Module III — carte d'accès et de ressources](/fr/reference/modules/iii-access-map/) — le signal de drift
  que consomment le classificateur de risque et la capacité de drift.
- [Honnêteté & limites](/fr/start/honesty-and-limits/) — pourquoi un statut, pas une certification.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — revoir un palier de risque suggéré.
- [Transférer l'audit vers Splunk](/fr/how-to/forward-audit-to-splunk/) — le flux continu du registre
  que l'auditeur revérifie.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — la couche intelligence
  et le bus d'événements.
