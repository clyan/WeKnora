import assert from 'node:assert/strict'
import test from 'node:test'
import type { CodeWikiRunPage } from '../../../api/code-wiki/index'
import { buildDocumentTree, containsDocument } from './documentTree'

function page(key: string, path?: string[] | null, order = 0): CodeWikiRunPage {
  return {
    id: key, run_id: 'run', page_key: key, title: key, page_type: 'synthesis',
    category: 'frontend_page', category_path: path, sort_order: order, content: '',
    summary: '', baseline_version: 0, validation_status: 'pending', status: 'pending',
  }
}

test('groups task documents into all directory levels and counts descendants', () => {
  const input = [
    page('orders', ['页面模块', 'app'], 0),
    page('users', ['页面模块', 'app'], 1),
    page('flow', ['架构总览', 'flows', 'app'], 4000),
  ]
  const tree = buildDocumentTree(input)
  assert.equal(tree.length, 2)
  const pages = tree[0]
  assert.equal(pages.kind, 'folder')
  if (pages.kind !== 'folder') return
  assert.equal(pages.title, '页面模块')
  assert.equal(pages.count, 2)
  assert.equal(pages.children.length, 1)
  assert.equal(pages.children[0].kind, 'folder')
  assert.ok(containsDocument(tree[1], 'flow'))
  assert.ok(!containsDocument(tree[0], 'flow'))
})

test('keeps pages without directories visible at root and respects server order', () => {
  const input = [page('later', ['API模块'], 20), page('legacy', null, 0), page('first', ['API模块'], 10)]
  const tree = buildDocumentTree(input)
  assert.equal(tree[0].kind, 'page')
  const folder = tree[1]
  assert.equal(folder.kind, 'folder')
  if (folder.kind !== 'folder') return
  assert.deepEqual(folder.children.map(node => node.kind === 'page' && node.page.page_key), ['first', 'later'])
  assert.deepEqual(input.map(item => item.page_key), ['later', 'legacy', 'first'], 'sorting cannot mutate polling state')
})

test('folder identities use complete segments and cannot collide on separators', () => {
  const tree = buildDocumentTree([page('one', ['a/b']), page('two', ['a', 'b']), page('three', ['__proto__'])])
  assert.equal(tree.length, 3)
  assert.equal(new Set(tree.map(node => node.key)).size, 3)
  assert.ok(containsDocument(tree[0], 'one'))
  assert.ok(!containsDocument(tree[0], 'two'))
})

test('status polling keeps directory identities and updates document state', () => {
  const item = page('orders', ['页面模块', 'app'])
  const before = buildDocumentTree([item])
  const after = buildDocumentTree([{ ...item, validation_status: 'generating' }, page('users', ['页面模块', 'app'])])
  assert.equal(before[0].key, after[0].key)
  const folder = after[0]
  if (folder.kind !== 'folder') throw new Error('expected directory')
  assert.equal(folder.count, 2)
  const repository = folder.children[0]
  if (repository.kind !== 'folder') throw new Error('expected repository directory')
  const document = repository.children[0]
  if (document.kind !== 'page') throw new Error('expected document')
  assert.equal(document.page.validation_status, 'generating')
})
