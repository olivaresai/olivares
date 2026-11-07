---
title: Documents
description: Conversion locale en Markdown avec MarkItDown ; originaux conservés, sans accès réseau.
---

Joignez un PDF, DOCX, PPTX, XLSX, HTML, CSV, JSON, XML, EPUB ou ZIP à une session, déposez-le dans Mes fichiers ou mentionnez-le avec `@chemin` dans le dossier de la session. Pour les espaces, utilisez `@"chemin avec espaces.pdf"`. Microsoft MarkItDown le convertit localement ; l’agent reçoit du Markdown comme contenu non fiable. Les images restent transmises comme images.

L’original est conservé et le Markdown est créé à côté, sans remplacer les fichiers existants. Documents est activé sur les nouvelles installations et peut être désactivé dans Paramètres > Modules. Un module désactivé ou un convertisseur absent refuse explicitement les nouvelles conversions.

`olivares files convert rapport.pdf` affiche le chemin de `rapport.pdf.md` ; `--json` renvoie `{"path":"..."}`. Codes de sortie : 0 pour la réussite, 1 pour un échec de conversion, 2 pour des arguments invalides. Un Markdown existant n’est jamais remplacé.

Le convertisseur n’a ni accès réseau, ni plugins, ni identifiants de fournisseur. Il reçoit uniquement les octets du document via stdin et exige Linux Landlock ABI 3 ou ultérieur. Limites : 32 MiB en entrée et en sortie, 30 secondes au total, 25 secondes de CPU et 768 MiB de mémoire virtuelle. Les pièces jointes gardent leur limite de 3,75 MiB. Les images ISO serveur et Docker incluent les dépendances verrouillées par hash ; aucun paquet n’est installé pendant la conversion.
