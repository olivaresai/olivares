#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Compare API, CLI, configuration and migration snapshots.

Snapshot: schema=olivares.userspace/1; openapi/openapi_beta are complete documents;
cli is the TestCLIRefDump object; config/environment map names to default values;
paths lists installed files, service names and package names; migrations records
compiled versions and SQL hashes; runtime records literal CLI exits and JSON. No credentials.
Candidate classifications pin one finding, its exact values, reason and evidence.
Exit 0: compared contracts compatible; 1: differences; 2: invalid snapshot.
"""
import argparse
import json
import sys
from pathlib import Path

METHODS = {'get', 'post', 'put', 'patch', 'delete', 'head', 'options'}


def resolve(schema, doc):
    seen = set()
    while isinstance(schema, dict) and '$ref' in schema:
        ref = schema['$ref']
        if not ref.startswith('#/') or ref in seen:
            raise ValueError('unresolved or cyclic schema reference: ' + ref)
        seen.add(ref)
        schema = doc
        for part in ref[2:].split('/'):
            schema = schema[part.replace('~1', '/').replace('~0', '~')]
    return schema


def schema_diff(old, new, odoc, ndoc, path, input_schema, seen=(), open_enums=False, warnings=None):
    old, new = resolve(old, odoc), resolve(new, ndoc)
    pair = (id(old), id(new), input_schema)
    if pair in seen:
        return []
    seen += (pair,)
    # A retained union branch preserves the old contract, even when nested.
    for keyword in ('oneOf', 'anyOf'):
        if keyword in old:
            return [finding for branch in old[keyword]
                    for finding in schema_diff(branch, new, odoc, ndoc, path, input_schema, seen, open_enums, warnings)]
        if keyword in new:
            choices = [schema_diff(old, branch, odoc, ndoc, path, input_schema, seen, open_enums, warnings)
                       for branch in new[keyword]]
            return min(choices, key=len) if choices else [path + ': empty union']
    findings = []
    for key in ('type', 'default'):
        if old.get(key) != new.get(key):
            findings.append(path + ': changed ' + key)
    if input_schema:
        for key in ('minimum', 'exclusiveMinimum', 'minLength', 'minItems', 'minProperties'):
            if key in new and (key not in old or new[key] > old[key]):
                findings.append(path + ': tightened ' + key)
        for key in ('maximum', 'exclusiveMaximum', 'maxLength', 'maxItems', 'maxProperties'):
            if key in new and (key not in old or new[key] < old[key]):
                findings.append(path + ': tightened ' + key)
        if new.get('pattern') != old.get('pattern') and new.get('pattern') is not None:
            findings.append(path + ': changed input pattern')
        if set(new.get('required') or []) - set(old.get('required') or []):
            findings.append(path + ': new required inputs')
        if new.get('additionalProperties') is False and old.get('additionalProperties') is not False:
            findings.append(path + ': additional inputs refused')
        if 'enum' in new and ('enum' not in old or not set(old['enum']) <= set(new['enum'])):
            findings.append(path + ': input enum narrowed')
    elif set(old.get('required') or []) - set(new.get('required') or []):
        findings.append(path + ': required output fields weakened')
    if not input_schema and 'enum' in old:
        if 'enum' not in new or not set(old['enum']) <= set(new['enum']):
            findings.append(path + ': output enum values removed')
        if set(new.get('enum', [])) - set(old['enum']):
            message = path + ': output enum expanded'
            if open_enums and warnings is not None:
                warnings.append(message + '; published SDKs decode generic JSON')
            else:
                findings.append(message)
    for name, field in old.get('properties', {}).items():
        after = new.get('properties', {}).get(name)
        if after is None:
            findings.append(path + '.' + name + ': field removed')
        else:
            findings += schema_diff(field, after, odoc, ndoc, path + '.' + name, input_schema, seen, open_enums, warnings)
    if 'items' in old:
        findings += schema_diff(old['items'], new.get('items', {}), odoc, ndoc, path + '[]', input_schema, seen, open_enums, warnings)
    for keyword in ('allOf', 'not', 'if', 'then', 'else', 'const'):
        if old.get(keyword) != new.get(keyword):
            findings.append(path + ': review changed ' + keyword)
    return findings


def api_diff(old, new, open_operations=(), warnings=None):
    findings = []
    for path, item in old['paths'].items():
        item = resolve(item, old)
        newitem = resolve(new['paths'].get(path, {}), new)
        for method in METHODS & item.keys():
            label = method.upper() + ' ' + path
            after = newitem.get(method)
            if after is None:
                findings.append(label + ': operation removed')
                continue
            before = item[method]
            if warnings is not None and after.get('deprecated') and not before.get('deprecated'):
                warnings.append(label + ': deprecated')
            parameters = {}
            added = {}
            for source, document, output in (([item, before], old, parameters), ([newitem, after], new, added)):
                for owner in source:
                    for entry in owner.get('parameters', []):
                        parameter = resolve(entry, document)
                        output[(parameter['in'], parameter['name'])] = parameter
            for key, parameter in parameters.items():
                if key not in added:
                    findings.append(label + ': parameter removed ' + repr(key))
                else:
                    findings += schema_diff(parameter.get('schema', {}), added[key].get('schema', {}), old, new, label + ' ' + repr(key), True)
            for key, parameter in added.items():
                if parameter.get('required') and not parameters.get(key, {}).get('required'):
                    findings.append(label + ': new required parameter ' + repr(key))
            bbody, abody = resolve(before.get('requestBody', {}), old), resolve(after.get('requestBody', {}), new)
            if abody.get('required') and not bbody.get('required'):
                findings.append(label + ': request body now required')
            for media, content in bbody.get('content', {}).items():
                other = abody.get('content', {}).get(media)
                if other is None:
                    findings.append(label + ': request media removed ' + media)
                else:
                    findings += schema_diff(content.get('schema', {}), other.get('schema', {}), old, new, label + ' request', True)
            for status, response in before.get('responses', {}).items():
                if status not in after.get('responses', {}):
                    findings.append(label + ': status removed ' + status)
                    continue
                response = resolve(response, old)
                other = resolve(after['responses'][status], new)
                for media, content in response.get('content', {}).items():
                    ac = other.get('content', {}).get(media)
                    if ac is None:
                        findings.append(label + ': response media removed ' + media)
                    else:
                        findings += schema_diff(content.get('schema', {}), ac.get('schema', {}), old, new,
                                                label + ' response ' + status, False,
                                                open_enums=label in open_operations, warnings=warnings)
    return findings


def json_diff(old, new, path):
    findings = []
    if type(old) is not type(new):
        return [path + ': JSON type changed']
    if isinstance(old, dict):
        for key, value in old.items():
            if key not in new:
                findings.append(path + '.' + key + ': JSON key removed')
            else:
                findings += json_diff(value, new[key], path + '.' + key)
    elif isinstance(old, list) and old:
        if not new:
            findings.append(path + ': JSON array lost its old rows')
        else:
            for row in old:
                choices = [json_diff(row, other, path + '[]') for other in new]
                findings += min(choices, key=len)
    return findings


def compare(old, new, warnings=None):
    findings = []
    for key in ('openapi', 'openapi_beta'):
        notices = []
        findings += [key + ': ' + finding for finding in api_diff(old[key], new[key],
                                                                 old.get('open_json_operations', []), notices)]
        if warnings is not None:
            warnings.extend(key + ': ' + notice for notice in notices)
    before = {c['path']: c for c in old['cli']['commands']}
    after = {c['path']: c for c in new['cli']['commands']}
    for path, command in before.items():
        if path not in after:
            findings.append(path + ': command removed')
            continue
        if warnings is not None and after[path].get('deprecated') and after[path]['deprecated'] != command.get('deprecated'):
            warnings.append(path + ': ' + after[path]['deprecated'])
        if command.get('runnable') and not after[path].get('runnable'):
            findings.append(path + ': no longer runnable')
        for alias in set(command.get('aliases', [])) - set(after[path].get('aliases', [])):
            findings.append(path + ': alias removed ' + alias)
        flags = {flag['name']: flag for flag in command.get('flags', [])}
        newflags = {flag['name']: flag for flag in after[path].get('flags', [])}
        for name, flag in flags.items():
            if name not in newflags:
                findings.append(path + ': flag removed ' + name)
            else:
                if warnings is not None and newflags[name].get('deprecated') and newflags[name]['deprecated'] != flag.get('deprecated'):
                    warnings.append(path + ' --' + name + ': ' + newflags[name]['deprecated'])
                for key in ('type', 'default', 'shorthand'):
                    if flag.get(key) != newflags[name].get(key):
                        if key == 'default' and 'effective_default' in flag and flag['effective_default'] == newflags[name].get('effective_default'):
                            if warnings is not None:
                                warnings.append(path + ': flag ' + name + ' metadata default changed; native effective default preserved')
                            continue
                        findings.append(path + ': flag ' + name + ' changed ' + key)
                if 'effective_default' in flag and flag['effective_default'] != newflags[name].get('effective_default'):
                    findings.append(path + ': flag ' + name + ' effective default changed')
        for name, flag in newflags.items():
            if flag.get('required') and not flags.get(name, {}).get('required'):
                findings.append(path + ': flag now required ' + name)
    for key in ('config', 'environment'):
        for name, default in old[key].items():
            if name not in new[key]:
                findings.append(key + ': key removed ' + name)
            elif new[key][name] != default:
                findings.append(key + ': changed default ' + name)
    findings += ['path removed: ' + path for path in set(old['paths']) - set(new['paths'])]
    for engine, history in old['migrations'].items():
        other = new['migrations'].get(engine, {})
        versions = history['versions']
        if other.get('versions', [])[:len(versions)] != versions:
            findings.append(engine + ': migration versions are not a forward-only prefix')
        for path, digest in history['files'].items():
            if other.get('files', {}).get(path) != digest:
                findings.append(engine + ': published migration removed or rewritten: ' + path)
    for name, result in old['runtime'].items():
        other = new['runtime'].get(name)
        if other is None:
            findings.append(name + ': literal CLI case removed')
            continue
        if result['exit'] != other['exit']:
            findings.append(name + ': CLI exit changed')
        if 'json' in result:
            if 'json' not in other:
                findings.append(name + ': CLI JSON unavailable')
            else:
                findings += json_diff(result['json'], other['json'], name)
        if 'text' in result and result['text'] != other.get('text'):
            findings.append(name + ': deterministic CLI text changed')
    return sorted(set(findings))


def load(path):
    value = json.loads(path.read_text())
    if value.get('schema') != 'olivares.userspace/1':
        raise ValueError('unsupported snapshot schema')
    for key in ('openapi', 'openapi_beta'):
        if not isinstance(value[key]['paths'], dict) or not value[key]['paths']:
            raise ValueError('missing API operations')
    if value['cli'].get('schema') != 'olivares.cli-ref/1' or not value['cli']['commands']:
        raise ValueError('missing CLI commands')
    for key in ('config', 'environment'):
        if not isinstance(value[key], dict) or not value[key]:
            raise ValueError('missing ' + key + ' inventory')
    if not isinstance(value['paths'], list) or not value['paths']:
        raise ValueError('missing installed path inventory')
    if set(value['migrations']) != {'sqlite', 'postgres'}:
        raise ValueError('both migration engines are required')
    for history in value['migrations'].values():
        versions = history['versions']
        if not versions or any(type(v) is not int or v <= 0 for v in versions):
            raise ValueError('invalid migration versions')
        if versions != sorted(set(versions)) or not isinstance(history['files'], dict) or not history['files']:
            raise ValueError('invalid migration inventory')
    if not isinstance(value['runtime'], dict) or not value['runtime']:
        raise ValueError('missing literal CLI cases')
    for result in value['runtime'].values():
        if type(result['exit']) is not int:
            raise ValueError('invalid CLI exit')
    return value


def value_at(snapshot, path):
    try:
        for key in path:
            if isinstance(key, dict):
                matches = [item for item in snapshot if all(item.get(k) == v for k, v in key.items())]
                if len(matches) != 1:
                    return None
                snapshot = matches[0]
            else:
                snapshot = snapshot[key]
        return snapshot
    except (KeyError, TypeError, AttributeError):
        return None


def classify(old, new, findings, warnings):
    classified, seen = [], set()
    for row in new.get('classifications', []):
        if (set(row) != {'finding', 'path', 'published', 'candidate', 'reason', 'evidence'}
                or any(not isinstance(row[key], str) or not row[key].strip()
                       for key in ('finding', 'reason', 'evidence'))
                or not isinstance(row['path'], list) or not row['path']
                or any(not isinstance(key, (str, dict)) or not key for key in row['path'])
                or row['published'] is None or row['candidate'] is None
                or row['finding'] in seen):
            raise ValueError('invalid or duplicate classification')
        seen.add(row['finding'])
        if (row['finding'] in findings + warnings
                and value_at(old, row['path']) == row['published']
                and value_at(new, row['path']) == row['candidate']):
            classified.append(row)
    accepted = {row['finding'] for row in classified}
    return ([finding for finding in findings if finding not in accepted],
            [warning for warning in warnings if warning not in accepted], classified)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('published', type=Path)
    parser.add_argument('candidate', type=Path)
    args = parser.parse_args()
    try:
        old, new = load(args.published), load(args.candidate)
        warnings = []
        findings = compare(old, new, warnings)
        findings, warnings, classified = classify(old, new, findings, warnings)
        print(json.dumps({'findings': findings, 'warnings': sorted(set(warnings)),
                          'classified': classified}, indent=2))
        return 1 if findings else 0
    except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
        print('userspace comparison unavailable: ' + str(error), file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
