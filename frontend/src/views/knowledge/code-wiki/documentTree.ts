import type { CodeWikiRunPage } from '@/api/code-wiki'

export type DocumentTreeNode =
  | { kind: 'page'; key: string; page: CodeWikiRunPage }
  | { kind: 'folder'; key: string; title: string; count: number; children: DocumentTreeNode[] }

// Use the actual directory path, never infer folders from titles or source
// filenames. Pages from older tasks without a path remain at the root.
export function buildDocumentTree(pages: CodeWikiRunPage[]): DocumentTreeNode[] {
  const roots: DocumentTreeNode[] = []
  const folders = new Map<string, Extract<DocumentTreeNode, { kind: 'folder' }>>()
  const ordered = pages.map((page, index) => ({ page, index }))
    .sort((a, b) => (a.page.sort_order ?? 0) - (b.page.sort_order ?? 0) || a.index - b.index)
  for (const { page } of ordered) {
    let children = roots
    const path: string[] = []
    for (const segment of page.category_path ?? []) {
      if (!segment.trim()) continue
      path.push(segment)
      const key = 'folder:' + JSON.stringify(path)
      let folder = folders.get(key)
      if (!folder) {
        folder = { kind: 'folder', key, title: segment, count: 0, children: [] }
        folders.set(key, folder)
        children.push(folder)
      }
      folder.count++
      children = folder.children
    }
    children.push({ kind: 'page', key: 'page:' + page.page_key, page })
  }
  return roots
}

export function containsDocument(node: DocumentTreeNode, pageKey: string): boolean {
  return node.kind === 'page'
    ? node.page.page_key === pageKey
    : node.children.some(child => containsDocument(child, pageKey))
}
