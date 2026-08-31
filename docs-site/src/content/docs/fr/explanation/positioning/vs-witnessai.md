---
title: Olivares AI face à WitnessAI
description: >-
  Une comparaison honnête et sourcée avec WitnessAI — le face-à-face le plus
  proche sur la gouvernance des agents IA au sein des IDE et des outils de
  développement. Une parité réelle sur la découverte d'agents et les allowlists
  MCP ; une différence claire et défendable pour l'acheteur régulé, auto-hébergé :
  application in-process, un registre de preuves cryptographique, et un plan de
  données qui ne quitte jamais votre périmètre.
sidebar:
  order: 8
---

La plupart des « concurrents » d'Olivares AI se situent dans un couloir adjacent —
tours de contrôle, passerelles, observabilité — et les
[autres pages de positionnement](/fr/explanation/positioning/market-context-and-sources/)
expliquent pourquoi ce sont des *et*, non des *ou*. **WitnessAI est le véritable
face-à-face.** Il gouverne les agents IA au sein de l'environnement de
développement : découverte des agents de code, application des listes d'outils
approuvés, et application de politiques à ce que font les agents. Cette page est
donc tenue à un niveau d'exigence supérieur — chaque affirmation sur WitnessAI
ci-dessous est une citation textuelle de leur propre site (récupéré le
2026-06-21), et là où leur site est silencieux nous disons *"non documenté"*,
jamais *"absent"*.

:::note[Comment lire cette page]
Nous comparons sur le **modèle d'architecture et de déploiement**, non sur une
liste de fonctionnalités, parce que c'est là que la différence est réelle et
durable. Sur les fonctionnalités où nous nous recoupons véritablement, nous le
disons et ne revendiquons **aucune** supériorité. Le différenciateur s'adresse à
un acheteur spécifique : l'organisation régulée ou air-gapped qui ne peut pas
envoyer ses données de gouvernance dans le cloud de quelqu'un d'autre.
:::

## Là où nous sommes à parité (et nous ne prétendrons pas le contraire)

WitnessAI fait un vrai travail dans deux domaines qu'Olivares couvre aussi. Nous
les traitons comme une **parité** et n'affirmons pas être meilleurs :

- **Découverte d'agents / shadow-AI.** WitnessAI annonce *"Find and catalog
  thousands of AI applications, agents, and MCP servers"* et, pour les
  développeurs, *"Discover apps like GitHub Copilot, Cursor, and hundreds of other
  AI dev tools across your network"* ([witness.ai](https://witness.ai/)). Olivares
  découvre et inventorie lui aussi les agents, modèles, serveurs MCP et outils.
  Point de vue différent — leur réseau, notre télémétrie-plus-audit read-first —
  mais le *résultat* de découverte est comparable, et nous ne prétendrons pas que
  notre catalogue est catégoriquement supérieur.
- **Allowlists MCP / gouvernance des outils approuvés.** WitnessAI : *"Enforce
  control of approved MCP servers and tools across every agent, IDE, and agentic
  app"* et *"Maintain an organization-wide approved-tool list of MCP servers and
  tools"* (witness.ai). Olivares gouverne lui aussi l'accès aux outils MCP
  ([gouvernance MCP](/fr/how-to/connectors/mcp-governance/)). Parité. Aucun des deux
  points de cette page n'est « nous mettons MCP en allowlist mieux qu'eux ».

Si la découverte d'agents et l'allowlisting MCP constituent l'intégralité de votre
besoin, le choix est serré sur le plan des capacités, et d'autres facteurs (modèle
de déploiement, prix, empreinte existante) devraient le trancher. Nous préférons le
dire plutôt que de surenchérir.

## Ce qu'est WitnessAI, dans leurs mots

Le modèle de WitnessAI est **au niveau du réseau et livré en cloud**, avec une
philosophie de contrôle explicitement *fondée sur l'intention* :

- **Au niveau du réseau, sans client.** *"See AI activity across your entire
  network without relying on browser extensions or endpoint clients"*, et une
  plateforme qui *"operates at the network level—no new SDKs, additional clients,
  or added exposure"* (witness.ai).
- **Politique fondée sur l'intention.** *"Traditional security sees text;
  WitnessAI sees intent"*, avec des *"intent-based ML engines that understand
  context, not just keywords"* (witness.ai). C'est un choix de conception réel et
  distinct, et une force pour le cas d'usage in-line et sensible au contenu.
- **Gouvernance d'agents attribuée à un humain.** *"every agent action maps back
  to a human identity"*, sous *"a single policy engine [that] governs both human
  and agent workforces"* (witness.ai).
- **Une histoire de souveraineté SaaS.** Ils traitent bel et bien le contrôle des
  données — *"a secure, single-tenant environment that ensures data sovereignty"*,
  *"single-tenant environment with your own key encryption"*, et *"regional
  sandboxes"* (witness.ai). C'est un modèle **côté cloud, single-tenant, à clé
  client**. C'est une vraie réponse à la résidence des données — et c'est une
  réponse *différente* de la nôtre, ce qui est le nœud ci-dessous.

Ce sont des capacités, sourcées et énoncées équitablement. La comparaison n'est pas
« ils sont faibles » ; c'est « nous sommes bâtis sur une architecture différente,
pour un acheteur différent ».

## Là où Olivares est structurellement différent

| Dimension | WitnessAI (selon leur site) | Olivares AI |
|---|---|---|
| **Déploiement** | Au niveau du réseau, livré en cloud ; single-tenant avec clés client et regional sandboxes. Auto-hébergé / on-prem / air-gapped **non documenté** | Auto-hébergé par défaut ; [air-gapped](/fr/how-to/air-gap-install/) pris en charge ; le plan de données ne quitte jamais votre périmètre |
| **Licence** | SaaS propriétaire ; open source **non documenté** | Open-core **AGPL**, à source disponible — auditable, aucun control plane SaaS dans votre chemin de conformité |
| **Point d'application** | Au niveau du réseau, avec *"enforcement at the tool call and MCP server level"* | In-process au runtime de l'agent — un [PEP deny-closed dans Claude Code](/fr/how-to/connectors/claude-code-hooks-pep/), plus des gates MCP et d'actuation |
| **Preuve** | *"detailed logging keeps you audit-ready"* — un registre cryptographique / immuable est **non documenté** | Registre en ajout seul, à chaînage de hachage, [signé Ed25519](/fr/reference/glossary/#audit-ledger), vérifiable hors machine, export OSCAL |
| **Intervention en direct** | Approbations human-in-the-loop / break-glass **non documenté** | [Approbations HITL](/fr/reference/glossary/#approbation-hitl), [break-glass](/fr/reference/glossary/#break-glass), et un [kill switch](/fr/reference/glossary/#kill-switch) sur les sessions en direct, deny-closed |
| **Modèle d'identité** | *"every agent action maps back to a human identity"* — cycle de vie NHI **non documenté** | Agents comme [identités non humaines](/fr/reference/glossary/#identity--nhi) de première classe avec provisionnement, blocage sur péremption, rotation et offboarding |

Chaque *"non documenté"* ci-dessus signifie exactement cela : il n'apparaît pas sur
les pages WitnessAI que nous avons lues. Ce n'est **pas** une affirmation que leur
produit manque de la capacité — seulement que nous n'affirmerons pas, en leur nom,
quelque chose que leur propre site n'énonce pas.

## Le coin défendable : l'acheteur régulé, auto-hébergé

Réduisez le tableau à l'essentiel et une différence est porteuse. Le contrôle des
données de WitnessAI est un **cloud single-tenant** avec vos clés ; celui
d'Olivares est un **control plane auto-hébergé** sur votre propre infrastructure — Linux,
Docker, Kubernetes, on-prem ou air-gapped — sans télémétrie obligatoire ni sortie du plan
de contrôle par défaut. Ne franchit votre périmètre que ce que **vous** configurez à cette
fin : les appels à vos API de modèles, les sorties SIEM/webhook que vous raccordez et, si
vous en provisionnez un, un fournisseur externe d'embeddings. Pour beaucoup d'acheteurs,
ces modèles sont équivalents. Pour l'acheteur **contractuellement ou légalement interdit d'un cloud
tiers** — défense, classifié, cloud souverain, certaines finances et santé
régulées — un modèle SaaS ou cloud single-tenant est disqualifié avant même que la
comparaison de fonctionnalités ne commence, et un control plane à source disponible,
auto-hébergeable et sans sortie du plan de contrôle par défaut est le seul qui passe les achats.

C'est le coin honnête : non pas « nous gouvernons mieux les agents », mais **« nous
les gouvernons sur une infrastructure que vous contrôlez entièrement, avec une
preuve cryptographique et une application in-process, pour l'acheteur qui ne peut
pas du tout utiliser de cloud »**. Combiné au PEP in-process et au registre
à altération détectable, c'est une position qu'un SaaS au niveau du réseau ne peut occuper en
ajoutant une fonctionnalité.

## Quand WitnessAI est le meilleur choix

Nous préférons que vous choisissiez bien plutôt que de nous choisir. WitnessAI est
probablement le meilleur choix quand :

- Vous voulez une **visibilité au niveau du réseau sans déployer ni exploiter** un
  control plane, et un SaaS single-tenant atteint votre barre de résidence des
  données.
- Votre priorité est une **classification de contenu in-line, fondée sur
  l'intention** sur le trafic IA d'entreprise général (et non spécifiquement le
  problème de l'agent-de-code-gouverné et de la preuve-à-altération-détectable sur lequel
  Olivares se centre).
- Vous n'avez **aucune exigence d'auto-hébergement, de disponibilité du code source
  AGPL, d'un registre de preuves cryptographique, ou de break-glass/HITL sur les
  sessions en direct** — les choses que leur site ne documente pas et autour
  desquelles Olivares est bâti.

Olivares gagne la décision quand le parc est **auto-hébergé ou air-gapped**, quand
la preuve doit permettre de **détecter les altérations et être vérifiable hors machine**, et quand
l'application doit vivre **à l'intérieur de l'agent**, deny-closed — sans que rien
de tout cela ne franchisse le cloud d'une autre entreprise.

:::caution[Sourcing et limites]
Chaque affirmation sur WitnessAI ici est citée de leur site public (pages
d'accueil, produit, développeur, conformité et contrôle) telles que récupérées le
2026-06-21 ; nous n'avons pas lu chaque page qu'ils publient, et *"non documenté"*
est circonscrit aux pages que nous avons lues. Le copy marketing n'est pas un
document d'architecture, et les capacités d'un produit changent. Si vous évaluez
les deux, vérifiez l'état actuel directement avec chaque fournisseur — c'est la
norme à laquelle toute cette
[section de positionnement](/fr/explanation/positioning/market-context-and-sources/)
se tient.
:::

## En lien

- [Gouverner Claude Code & Codex authentifiés par abonnement](/fr/explanation/positioning/governing-subscription-authed-agents/)
  — comment l'application in-process fonctionne réellement.
- [Où Olivares s'inscrit face à votre passerelle / Guardrails](/fr/explanation/positioning/where-olivares-fits-vs-your-gateway/)
  — la même discipline « nous ne concurrençons pas sur le chemin des requêtes ».
- [Où Olivares s'inscrit avec votre IdP](/fr/explanation/architecture/where-it-fits-with-your-idp/)
  — la fédération d'identité en lecture seule derrière le modèle NHI.
