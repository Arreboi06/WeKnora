export type WorkbenchFileRoot = 'workspace' | 'input' | 'output'

export interface WorkbenchFileRef {
  file_ref_version: number
  root: WorkbenchFileRoot
  segments: string[]
  artifact_id?: string
  version?: number
}

export interface WorkbenchFileEntry {
  name: string
  type: string
  size_bytes: number
  mod_time?: string
  ref: WorkbenchFileRef
}

export type WorkbenchPreviewClass =
  | 'presentation_page'
  | 'html_active'
  | 'table_csv'
  | 'download_only'

const WORKBENCH_FILE_ROOTS = new Set<WorkbenchFileRoot>(['workspace', 'input', 'output'])
const SAFE_SEGMENT = /^[^/\\:\x00]+$/
const RESERVED_WINDOWS_NAME = /^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|$)/i
const PREVIEW_CSP_BASE = [
  "default-src 'none'",
  'img-src data: blob:',
  "style-src 'unsafe-inline'",
  'font-src data:',
  "media-src 'none'",
  "object-src 'none'",
  "frame-src 'none'",
  "worker-src 'none'",
  "manifest-src 'none'",
  "connect-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
].join('; ')

export function rootWorkbenchFileRef(root: WorkbenchFileRoot, segments: string[] = []): WorkbenchFileRef {
  if (!WORKBENCH_FILE_ROOTS.has(root) || !Array.isArray(segments) || segments.some((segment) => !isSafeSegment(segment))) {
    throw new Error('invalid Workbench file segment')
  }
  return {
    file_ref_version: 1,
    root,
    segments: [...segments],
  }
}

export function childWorkbenchFileRef(parent: WorkbenchFileRef, name: string): WorkbenchFileRef | null {
  if (!parent || parent.file_ref_version !== 1 || !WORKBENCH_FILE_ROOTS.has(parent.root) || !isSafeSegment(name)) return null
  if (!Array.isArray(parent.segments) || parent.segments.some((segment) => !isSafeSegment(segment))) return null
  return {
    file_ref_version: 1,
    root: parent.root,
    segments: [...parent.segments, name],
  }
}

export function canPublishWorkbenchArtifact(entry: WorkbenchFileEntry | null | undefined, commandId: string | null | undefined): boolean {
  return Boolean(
    entry &&
    entry.type === 'file' &&
    entry.ref?.file_ref_version === 1 &&
    entry.ref.root === 'output' &&
    entry.ref.segments.length > 0 &&
    entry.ref.segments.every(isSafeSegment) &&
    typeof commandId === 'string' &&
    commandId.trim(),
  )
}

export function canPublishPresentationCandidate(entry: WorkbenchFileEntry | null | undefined, commandId: string | null | undefined): boolean {
  return canPublishWorkbenchArtifact(entry, commandId) && /\.pptx$/i.test(entry?.name || '')
}

export function previewSandboxMode(previewClass: WorkbenchPreviewClass | string | null | undefined): 'allow-scripts' | '' | null {
  switch (previewClass) {
    case 'html_active':
      return 'allow-scripts'
    case 'table_csv':
    case 'presentation_page':
      return ''
    default:
      return null
  }
}

export function hardenWorkbenchPreviewHTML(source: string, previewClass: WorkbenchPreviewClass | string): string {
  const sandbox = previewSandboxMode(previewClass)
  if (sandbox === null) throw new Error('preview class is not renderable')
  const scriptPolicy = previewClass === 'html_active' ? "script-src 'unsafe-inline'" : "script-src 'none'"
  const meta = '<meta http-equiv="Content-Security-Policy" content="' + PREVIEW_CSP_BASE + '; ' + scriptPolicy + '">'
  const html = typeof source === 'string' ? source.replace(/^\uFEFF/, '') : ''
  const head = /<head(?:\s[^>]*)?>/i.exec(html)
  if (head?.index !== undefined) {
    const offset = head.index + head[0].length
    return html.slice(0, offset) + meta + html.slice(offset)
  }
  const root = /<html(?:\s[^>]*)?>/i.exec(html)
  if (root?.index !== undefined) {
    const offset = root.index + root[0].length
    return html.slice(0, offset) + '<head>' + meta + '</head>' + html.slice(offset)
  }
  return '<!doctype html><html><head>' + meta + '</head><body>' + html + '</body></html>'
}

export function formatWorkbenchBytes(size: number): string {
  if (!Number.isFinite(size) || size <= 0) return '0 B'
  if (size < 1024) return Math.round(size) + ' B'
  if (size < 1024 * 1024) return (size / 1024).toFixed(1) + ' KB'
  if (size < 1024 * 1024 * 1024) return (size / (1024 * 1024)).toFixed(1) + ' MB'
  return (size / (1024 * 1024 * 1024)).toFixed(1) + ' GB'
}

function isSafeSegment(segment: unknown): segment is string {
  if (typeof segment !== 'string' || segment.length === 0 || segment.length > 255 || segment.trim() !== segment) return false
  if (segment === '.' || segment === '..' || segment.endsWith('.') || segment.endsWith(' ')) return false
  return SAFE_SEGMENT.test(segment) && !RESERVED_WINDOWS_NAME.test(segment)
}
