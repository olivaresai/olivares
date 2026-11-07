---
title: Documentos
description: Conversión local a Markdown con MarkItDown; conserva los originales, sin acceso a la red.
---

Adjunta un PDF, DOCX, PPTX, XLSX, HTML, CSV, JSON, XML, EPUB o ZIP a una sesión, súbelo a Mis archivos o menciónalo con `@ruta` dentro de la carpeta de la sesión. Para espacios, usa `@"ruta con espacios.pdf"`. Microsoft MarkItDown lo convierte localmente y el agente recibe Markdown como contenido no confiable. Las imágenes siguen llegando como imágenes.

El original se conserva y el Markdown se guarda a su lado, sin reemplazar archivos anteriores. Documentos está activado en instalaciones nuevas y puede desactivarse en Configuración > Módulos. Si está desactivado o falta el conversor, las conversiones nuevas se rechazan con un error explícito.

`olivares files convert informe.pdf` imprime la ruta de `informe.pdf.md`; `--json` devuelve `{"path":"..."}`. Los códigos de salida son 0 (éxito), 1 (fallo de conversión) y 2 (argumentos inválidos). Nunca reemplaza un Markdown existente.

El conversor no tiene acceso a la red, complementos ni credenciales de proveedores. Solo recibe los bytes del documento por stdin. Requiere Linux Landlock ABI 3 o posterior. Límites: 32 MiB de entrada y salida, 30 segundos de tiempo total, 25 segundos de CPU y 768 MiB de memoria virtual. Los adjuntos conservan el límite de 3,75 MiB. Las imágenes de servidor ISO y Docker incluyen las dependencias fijadas por hash; no se instalan paquetes durante la conversión.
