import assert from 'node:assert/strict'
import { test } from 'node:test'
import { duplicateGroupHeaderNames } from './group-header-rules.ts'

test('set, default, and remove share one case-insensitive name', () => {
  const headers = [
    { name: 'X-Tenant', action: 'set' },
    { name: 'User-Agent', action: 'default' },
  ]
  assert.equal(duplicateGroupHeaderNames(headers, ''), false)
  assert.equal(duplicateGroupHeaderNames(headers, 'x-tenant'), true)
  assert.equal(duplicateGroupHeaderNames(headers, 'user-agent'), true)
  assert.equal(duplicateGroupHeaderNames([{ name: 'X-A' }, { name: 'x-a' }], ''), true)
  assert.equal(duplicateGroupHeaderNames([{ name: 'X-A' }], 'X-A\r\nx-b'), true)
  assert.equal(duplicateGroupHeaderNames([{ name: 'X-A' }], 'x-b\nX-B'), true)
  assert.equal(duplicateGroupHeaderNames([{ name: 'X-A' }], '\n  \n'), false)
  assert.equal(duplicateGroupHeaderNames([{ name: '  ' }, { name: '' }], 'not a header'), false)
})
