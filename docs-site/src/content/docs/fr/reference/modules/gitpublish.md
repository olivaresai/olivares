---
title: "Publication Git gouvernée"
description: "Envoyer des commits, ouvrir des pull requests et fusionner avec des liaisons Git approuvées, une autorisation actuelle et des résultats conservés."
---

Le module gitpublish permet la publication gouvernée sur GitHub et GitLab. Il fait partie de Community.

Chaque effet exige une cible, un dépôt et des identifiants approuvés, ainsi qu’une autorisation actuelle. Sans les liaisons requises, l’exécutable Git ou l’autorisation, la publication est refusée. Enregistrer le module ne rend pas un dépôt prêt à publier.

L’espace de noms de l’API est `/v1/m/gitpublish`. Il expose les cibles, les intentions de push, de pull request et de fusion, le rapprochement et les observations.

Un résultat distant incertain n’autorise pas une nouvelle tentative. Le module conserve séparément les demandes, les observations et les accusés de réception. Une opération distante déjà envoyée peut coïncider avec une révocation locale ultérieure.

[Référence de l’API des modules](/fr/reference/api-beta/).
