---
title: Dokumente
description: Lokale Markdown-Konvertierung mit MarkItDown; Originaldateien bleiben erhalten, kein Netzwerkzugriff.
---

Hängen Sie PDF, DOCX, PPTX, XLSX, HTML, CSV, JSON, XML, EPUB oder ZIP an eine Sitzung an, laden Sie die Datei in Meine Dateien hoch oder nennen Sie sie mit `@pfad` im Sitzungsordner. Für Leerzeichen verwenden Sie `@"pfad mit leerzeichen.pdf"`. Microsoft MarkItDown konvertiert lokal; der Agent erhält Markdown als nicht vertrauenswürdigen Dokumentinhalt. Bilder werden weiterhin als Bilder übertragen.

Das Original bleibt erhalten. Markdown wird daneben gespeichert, ohne vorhandene Dateien zu ersetzen. Dokumente ist bei neuen Installationen aktiviert und lässt sich unter Einstellungen > Module ausschalten. Bei deaktiviertem Modul oder fehlendem Konverter werden neue Konvertierungen ausdrücklich abgelehnt.

`olivares files convert bericht.pdf` gibt den Pfad zu `bericht.pdf.md` aus; `--json` liefert `{"path":"..."}`. Exitcodes: 0 bei Erfolg, 1 bei Konvertierungsfehlern, 2 bei ungültigen Argumenten. Vorhandenes Markdown wird nie ersetzt.

Der Konverter hat keinen Netzwerkzugriff, keine Plugins und keine Anbieter-Zugangsdaten. Er erhält nur Dokumentbytes über stdin und benötigt Linux Landlock ABI 3 oder neuer. Grenzen: je 32 MiB Ein- und Ausgabe, 30 Sekunden Laufzeit, 25 Sekunden CPU und 768 MiB virtueller Speicher. Für Anhänge gelten weiterhin 3,75 MiB. Server-ISO- und Docker-Images enthalten per Hash fixierte Abhängigkeiten; während der Konvertierung werden keine Pakete installiert.

## Scanned pages and OCR

OCR ist optional und standardmäßig ausgeschaltet. Aktivieren Sie Scanned documents (OCR) unter Einstellungen > Module und konfigurieren Sie `document_ocr` in `OLIVARES_SOURCES_CONFIG`. Nur Seiten ohne Textschicht werden an den lokalen Endpunkt gesendet. Ohne OCR schlägt eine PDF-Datei ohne Textschicht mit dieser Fehlermeldung fehl: `this page has no text layer; OCR is off (Settings > Modules)`. Bilder werden bei aktiviertem OCR als Markdown übertragen. Modellgewichte müssen vorab mit SHA-256 fixiert bereitgestellt werden; während einer Sitzung wird nichts heruntergeladen.

[Configuration and endpoint privacy rules](/reference/modules/documents/#scanned-pages-and-ocr).
