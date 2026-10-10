// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
const assert = require('node:assert/strict');
const { createRequire } = require('node:module');
const { test } = require('node:test');

const fromBackstage = createRequire(require.resolve('@backstage/core-components'));
const fromTable = createRequire(fromBackstage.resolve('@material-table/core'));
const DataManager = fromTable('./utils/data-manager').default;

test('Backstage tables generate row keys and retain selection after refresh', () => {
  const manager = new DataManager();
  manager.setData([{ id: 'first' }, { id: 'second' }]);
  const keys = manager.data.map(row => row.tableData.uuid);
  for (const key of keys) {
    assert.match(key, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  }
  assert.notEqual(keys[0], keys[1]);
  manager.data[0].tableData.checked = true;
  manager.setData([{ id: 'first', name: 'updated' }, { id: 'second' }]);
  assert.deepEqual(manager.data.map(row => row.tableData.uuid), keys);
  assert.equal(manager.data[0].tableData.checked, true);
  assert.equal(manager.selectedCount, 1);
});

test('the table UUID dependency rejects out-of-bounds name-based writes', () => {
  const { v3, v5 } = fromTable('uuid');
  for (const generate of [v3, v5]) {
    const buffer = new Uint8Array(16).fill(0xaa);
    for (const offset of [-1, 1]) {
      assert.throws(() => generate('table-row', generate.DNS, buffer, offset), RangeError);
      assert.deepEqual(buffer, new Uint8Array(16).fill(0xaa));
    }
  }
});
