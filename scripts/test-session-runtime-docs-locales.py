#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the documentation checker's real locale reader and regex construction."""

import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
SCRIPTS = ROOT / 'docs-site/scripts'


class LocaleReaderTests(unittest.TestCase):
    def run_reader(self, mutate=None):
        with tempfile.TemporaryDirectory(prefix='session-docs-locales-') as tmp:
            tmp = Path(tmp)
            shutil.copytree(SCRIPTS / 'locales', tmp / 'locales')
            if mutate:
                path = tmp / 'locales/fr.json'
                data = json.loads(path.read_text())
                mutate(data)
                path.write_text(json.dumps(data))
            # Run the production locale reader, before the unrelated page walk.
            source = (SCRIPTS / 'check-session-runtime-docs.mjs').read_text()
            reader = source.split('async function checkPages(dir) {', 1)[0]
            reader += '\nconsole.log(JSON.stringify({staleClaims: staleClaims.map(r => ({source: r.source, flags: r.flags})), noProvider: Object.fromEntries(Object.entries(noProvider).map(([locale, r]) => [locale, {source: r.source, flags: r.flags}]))}))\n'
            path = tmp / 'reader.mjs'
            path.write_text(reader)
            return subprocess.run(['node', str(path)], capture_output=True,
                                  text=True, timeout=20)

    def test_actual_reader_preserves_every_regex(self):
        result = self.run_reader()
        self.assertEqual(result.returncode, 0, result.stderr)
        actual = json.loads(result.stdout)
        locales = ('en', 'de', 'es', 'fr', 'ja', 'ru', 'zh')
        data = {locale: json.loads((SCRIPTS / f'locales/{locale}.json').read_text())
                for locale in locales}
        self.assertEqual(actual['staleClaims'], [
            {key: data[locale][key] for key in ('source', 'flags')}
            for locale in locales])
        self.assertEqual(actual['noProvider'], {
            locale: data[locale]['noProvider'] for locale in locales})

    def test_missing_or_malformed_regex_data_is_refused(self):
        for entry in ('staleClaims', 'noProvider'):
            for key, value in (('source', None), ('source', ''), ('source', 7),
                               ('flags', None), ('flags', 7)):
                with self.subTest(entry=entry, key=key, value=value):
                    def mutate(data):
                        pattern = data if entry == 'staleClaims' else data[entry]
                        if value is None:
                            pattern.pop(key)
                        else:
                            pattern[key] = value
                    result = self.run_reader(mutate)
                    self.assertNotEqual(result.returncode, 0, result.stdout)
                    self.assertIn('Invalid fr locale regex', result.stderr)


if __name__ == '__main__':
    unittest.main()
