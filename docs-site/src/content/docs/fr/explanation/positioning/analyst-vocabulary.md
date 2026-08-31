---
title: Le vocabulaire des analystes, cartographié honnêtement
description: >-
  Le vocabulaire 2026 des analystes pour la gouvernance de l'IA — prolifération
  d'agents, guardian agents, AI TRiSM, discover/observe/govern/secure — défini,
  attribué là où il a une source, et mis en regard de ce qu'Olivares AI fait
  réellement et ne fait pas.
sidebar:
  order: 2
---

Si vous évaluez des outils d'IA, vous avez croisé ces mots : **prolifération
d'agents (agent sprawl)**, **guardian agents**, **AI TRiSM**, **discover / observe /
govern / secure**. Ce sont des raccourcis utiles, et un acheteur en 2026 attend d'un
fournisseur qu'il les emploie. Ils sont aussi faciles à détourner — pour laisser
entendre qu'un produit *est* une catégorie alors qu'il se contente d'en être proche.

Cette page fait trois choses : elle **définit** chaque terme, elle l'**attribue** là
où il a un véritable propriétaire, et elle **dit clairement** lesquels décrivent
Olivares AI et lesquels nous nous contentons d'approcher. Pour les chiffres qui
étayent le marché sous-jacent, voir
[Contexte de marché et sources](/fr/explanation/positioning/market-context-and-sources/).

## Prolifération d'agents (agent sprawl)

**Ce que cela signifie.** La prolifération incontrôlée d'agents IA, de copilotes, de
serveurs MCP et d'automatisations à travers une organisation — créés par différentes
équipes, avec différents identifiants, touchant différents systèmes, plus vite que
quiconque ne tient un inventaire. Le résultat : des agents inconnus avec des accès
inconnus.

**Cela nous décrit-il ?** Cela décrit le *problème pour lequel nous existons*. La
première mission d'Olivares AI est de rendre la prolifération visible : il
**découvre** les agents, modèles, serveurs MCP et outils de votre parc (estate) et
construit une
[carte d'accès en lecture/écriture](/fr/explanation/#laccess-map--read-first-minimal-data-permitted-vs-observed)
de ce que chacun peut atteindre — read-first, données minimales, sur **votre**
infrastructure. Le
[diff Permis-vs-Observé](/fr/reference/glossary/#observed--permitted) transforme alors
« nous avons beaucoup d'agents » en « voici ceux qui utilisent un accès que personne
n'a accordé ». La prolifération est la maladie ; un inventaire exact et attribué est
le premier traitement.

## Guardian agents

**Ce que cela signifie.** Le terme de **Gartner** désignant les capacités d'IA qui
surveillent, supervisent ou interviennent sur *d'autres* agents IA. Gartner prévoit
que les technologies de guardian agents représenteront **10–15 % du marché de l'IA
agentique d'ici 2030** (communiqué de presse Gartner, 2025 ; voir
[sources](/fr/explanation/positioning/market-context-and-sources/)).

**Cela nous décrit-il ? Avec prudence.** Olivares AI produit le *résultat de
gouvernance et de supervision* que vise la catégorie — observer le comportement des
agents, diffuser le permis face à l'observé, contrôler les actions en deny-closed, et
tout consigner dans un registre à altération détectable. Mais nous ne sommes
**pas** un agent runtime autonome qui raisonne sur d'autres agents dans le chemin des
requêtes. Nous sommes un **control plane read-first** qui se situe *en dehors* du
chemin de données : nous observons via la télémétrie, les journaux d'audit natifs et
un filet de sécurité noyau eBPF, et nous appliquons aux barrières bien définies
(approbations, le
[PEP par hooks Claude Code](/fr/how-to/connectors/claude-code-hooks-pep/), coupe-circuits)
— pas en insérant un proxy IA dans chaque appel. Si « guardian agent » signifie *une
gouvernance de supervision sur votre parc d'agents*, alors oui. Si cela signifie *un
LLM monté en garde en ligne (inline)*, c'est une architecture différente, et nous ne
le revendiquerons pas.

## AI TRiSM

**Ce que cela signifie.** **AI TRiSM** — *AI Trust, Risk and Security Management* —
est un **cadre forgé et détenu par Gartner** pour gérer la confiance, le risque et la
sécurité de l'IA tout au long de son cycle de vie. Tel qu'on le résume couramment, il
couvre la **gouvernance** et l'**inspection et l'application à l'exécution (runtime)**
de l'IA, aux côtés de la gouvernance de l'information et de la sécurité de
l'infrastructure.

:::caution[Note d'attribution]
Le cadre AI TRiSM, sa taxonomie de couches et toutes ses définitions sont une
**recherche propriétaire de Gartner**. Les reformulations publiques (y compris les
noms de couches et les diagrammes) proviennent typiquement de **reproductions sous
licence**. Nous décrivons AI TRiSM au niveau du *thème* et mettons nos capacités en
regard de ces thèmes ; nous ne **reproduisons pas** le modèle exact de Gartner, ne
revendiquons aucune conformité à celui-ci, ni n'impliquons un quelconque aval de
Gartner.
:::

**Comment nous nous y rattachons (niveau thématique).**

- **Gouvernance** — rédaction de politiques, classification du risque (palier UE ×
  fonction NIST), approbations/HITL, gestion-as-code, et le catalogue de cadres du
  [module conformité](/fr/reference/modules/xiii-compliance/).
- **Inspection à l'exécution** — la carte d'accès et la dérive Permis-vs-Observé, les
  constats de garde-fous/anomalies, les chronologies de session — tout cela read-first
  et hors bande (out-of-band).
- **Application à l'exécution** — barrières en deny-closed là où nous nous situons
  *effectivement* dans un chemin de décision : approbations, le PEP par hooks Claude
  Code, le contrôle des outils MCP, les coupe-circuits.
- **Gouvernance de l'information** — découverte de PII/sensibilité sur les bases de
  connaissances gouvernées, attestation de résidence des données, rétention et
  conservation à des fins légales (legal-hold).

Nous utilisons AI TRiSM comme une *carte de l'espace du problème qu'un acheteur
connaît déjà*, pour montrer la couverture — pas comme un label.

## Discover / observe / govern / secure

**Ce que cela signifie.** La séquence de verbes que les analystes et fournisseurs
emploient pour décrire le cycle de vie de la gouvernance de l'IA : d'abord
**découvrir** ce qui existe, puis **observer** ce que cela fait, puis **gouverner**
ce qui est autorisé, puis **sécuriser** l'ensemble du parc.

**Cela nous décrit-il ?** Oui — c'est proche de notre propre récit produit, qu'il
vaut la peine d'énoncer dans nos termes exacts pour que la correspondance soit
honnête :

| Verbe d'analyste | Ce qu'Olivares AI fait réellement |
|---|---|
| **Discover** | Inventaire des agents, modèles, serveurs MCP et outils à travers le parc. |
| **Observe** | La carte d'accès R/RW — read-first, données minimales, avec une confiance d'attribution par arête ; les chemins coopératifs (OTel, hooks) corroborés par l'audit natif (pgAudit, CloudTrail) et un filet de sécurité eBPF. |
| **Govern** | Dérive Permis-vs-Observé, politiques + approbations/HITL, barrières d'actuation en deny-closed, gestion-as-code. |
| **Secure** | Garde-fous, le registre d'audit à altération détectable, coupe-circuits, preuves de conformité — **et** l'auto-hébergement sans télémétrie obligatoire ni sortie du plan de contrôle par défaut. Ne franchit votre périmètre que ce que vous configurez à cette fin : les appels à vos API de modèles, les sorties SIEM/webhook que vous raccordez et, si vous en provisionnez un, un fournisseur externe d'embeddings. |

La réserve honnête qui traverse les quatre : **la fidélité est paliérisée**.
L'observation est nette pour les bases SQL, les magasins d'objets et les entrepôts ;
avec pertes pour les magasins de documents et de vecteurs ; et impossible à atteindre
passivement pour certains systèmes. La carte
[montre sa confiance](/fr/reference/glossary/#attribution-confiance) plutôt que
d'inventer une attribution qu'elle n'a pas.

## Les trois axes que ce vocabulaire désigne

Retirez les étiquettes et les trois mêmes différenciateurs subsistent — les axes que
le marché a laissés ouverts et auxquels le discours doit toujours revenir :

1. **La vérité-terrain depuis le plan de données.** Nous ne croyons pas un agent sur
   parole quant à ce qu'il a touché. Nous **corrélons** le signal coopératif (OTel,
   MCP, hooks) face au propre registre du système — pgAudit classant les lectures et
   les écritures, CloudTrail exposant l'accès au magasin d'objets — et un filet de
   sécurité noyau eBPF pour le cas non coopératif. Cette corrélation est ce qui fait
   du Permis-vs-Observé un *fait*, pas une auto-déclaration.
2. **Une application deny-closed sur l'agent de développement local.** La plupart des
   outils se contentent d'*observer* Claude Code. Olivares AI le **gouverne** aussi :
   le PEP par hooks transforme la politique en une décision deny-closed au niveau de
   l'agent, et non en une ligne de journal a posteriori.
3. **Souveraineté.** Auto-hébergé, source-available **AGPL** — le plan de données ne
   quitte jamais votre périmètre et il n'y a pas de control plane SaaS dans votre
   chemin de conformité.

Chacun des termes ci-dessus est au service de ces trois éléments. Lorsqu'une page
d'ici emploie un mot d'analyste, c'est pour rejoindre l'acheteur là où il se trouve —
puis pour renvoyer à l'une de ces trois choses que le produit fait réellement.
