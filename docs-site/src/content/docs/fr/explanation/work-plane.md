---
title: Le plan de travail
description: >-
  Comment les agents et les sessions coordonnent le travail dans Olivares AI — éléments
  de travail, messages, accusés de réception et transferts —, ce qui est réel et durable
  aujourd'hui et ce qui reste délibérément non câblé. La moitié du produit qui n'est pas
  la carte d'accès.
---

La majeure partie de cette documentation traite de **ce qu'un agent peut atteindre** : la
carte d'accès, les permissions et l'écart entre *Permitted* et *Observed*. Cette page traite
de l'autre moitié — **la manière dont les agents et les sessions coordonnent le travail
lui-même** —, celle que le reste du site n'a jusqu'ici décrite que sous la forme d'une liste
de commandes et d'événements.

Le problème auquel elle répond n'a rien d'hypothétique. C'est celui que ce projet a subi
pendant son propre développement : des sessions incapables de se voir, des états qui
divergent entre elles, du travail fait deux fois et des décisions qui ne vivent que dans le
terminal d'une personne et disparaissent quand il se ferme. Un plan de contrôle qui gouverne
l'*accès* et ne dit rien du *travail* laisse cette lacune exactement là où elle était.

## Ce qu'est un élément de travail

Un **élément de travail** est une unité de travail dotée d'un responsable, d'un état et d'un
enregistrement durable. Ce n'est ni un message de discussion ni un ticket dans le gestionnaire
de quelqu'un d'autre : il réside dans le même stockage que le ledger d'audit. Il est donc
possible de déterminer plus tard ce qui lui est arrivé par les mêmes moyens que pour tout ce
que le plan de contrôle enregistre.

Trois primitives l'entourent :

| Primitive | Fonction |
|---|---|
| **Message** | Un participant communique durablement quelque chose à un autre — il ne le diffuse pas dans un journal que personne ne lit |
| **Accusé de réception** | Le destinataire enregistre qu'il a *pris en charge* le message. « Lu » et « répondu » cessent de désigner la même chose |
| **Transfert** | La responsabilité d'un élément de travail change de main, avec la raison jointe au changement |

L'accusé de réception mérite qu'on s'y attarde. La coordination échoue bien plus souvent
parce qu'un message a été vu sans être suivi d'effet que parce qu'il n'a jamais été livré ;
un système incapable de distinguer ces cas ne peut pas non plus vous dire lequel s'est produit.

## Ce qui est réel aujourd'hui et ce qui ne l'est pas

:::caution[Lisez cette section avant de vous appuyer dessus]
Les primitives ci-dessus sont **implémentées et durables**. Leur portée est
**délibérément plus étroite** que l'idée, et cette limite est imposée dans le code plutôt
que promise en prose. Trois limites, énoncées sans détour :
:::

**1 · La coordination est limitée à un workflow, et le plan public de communication reste
délibérément non câblé.** Les messages, les accusés de réception et les transferts sont réels dans
l'exécution propre du workflow. Le plan général de communication entre tous les éléments
*n'est pas* connecté — et il ne s'agit pas d'un oubli qui attend d'être remarqué : un test
de démarrage vérifie quelles sources d'autorité `boot` peut câbler et **échoue si une autre
apparaît** (`cmd/olivares/communicationauthorityboot_test.go`,
`TestBootWiresExactCommunicationRequestAuthoritySourcesOnly`). Le câbler par accident
produit un test rouge, pas une surprise en production.

**2 · Le dispatch entre agents n'est monté qu'avec une destination autorisée.** L'exécuteur
de travail distant est construit avec une porte d'approbation devant lui
(`cmd/olivares/wire.go`) ; aucun chemin ne distribue du travail à un pair arbitraire
simplement parce qu'un fichier de configuration l'a gentiment demandé.

**3 · Le mode shadow et l'autorité finale sur le travail N'EXISTENT PAS.** Ils ne sont ni
« bientôt disponibles » ni « partiels » : ils sont absents. Un déploiement ne peut pas
aujourd'hui donner au plan de travail le dernier mot sur une session, et rien dans le
produit ne doit être interprété comme offrant cette capacité. Toute mise en œuvre
hypothétique devrait être accompagnée de la preuve de son fonctionnement — une fenêtre de
comparaison avec les sources existantes, pas un changement de version.

## Pourquoi les limites sont écrites ici

Parce que l'alternative serait pire pour vous. Une page qui décrirait la conception et vous
laisserait découvrir la limite au moment de l'intégration vous coûterait l'après-midi ; une
page qui appellerait la moitié absente « roadmap » ferait précisément le type d'affirmation
que ce projet refuse. La page [honnêteté et limites](/fr/start/honesty-and-limits/) énonce la
règle générale ; ceci est son application à la surface la plus récente du produit.

## Où aller ensuite

- [Vue d'ensemble des modules](/fr/reference/modules/overview/) — la place de
  l'orchestration parmi les autres modules.
- [Référence de l'orchestration](/fr/reference/modules/iv-orchestration/) — le module qui
  possède l'exécution des workflows.
- [Référence du bus d'événements](/fr/reference/events/) — les événements émis par le plan
  de travail, sous la forme d'un contrat AsyncAPI.
- [Construire un workflow gouverné](/fr/how-to/build-a-workflow/) — le parcours pratique,
  une fois que vous savez ce que le plan fait et ne fait pas.
