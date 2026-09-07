import assert from 'node:assert/strict'
import test from 'node:test'

import {
  canPublishPresentationCandidate,
  canPublishWorkbenchArtifact,
  childWorkbenchFileRef,
  formatWorkbenchBytes,
  hardenWorkbenchPreviewHTML,
  rootWorkbenchFileRef,
  previewSandboxMode,
  type WorkbenchFileEntry,
} from './workbenchFiles'

const outputFile: WorkbenchFileEntry = {
  name: 'report.csv',
  type: 'file',
  size_bytes: 2048,
  mod_time: '2026-09-08T00:00:00Z',
  ref: rootWorkbenchFileRef('output', ['report.csv']),
}

test('T2-L10 file references stay within their declared root', () => {
  assert.deepEqual(rootWorkbenchFileRef('workspace'), {
    file_ref_version: 1,
    root: 'workspace',
    segments: [],
  })
  assert.deepEqual(childWorkbenchFileRef(rootWorkbenchFileRef('output'), 'report.csv'), outputFile.ref)
  assert.equal(childWorkbenchFileRef(outputFile.ref, '../escape'), null)
  assert.equal(childWorkbenchFileRef(outputFile.ref, 'nested/name.csv'), null)
  assert.equal(childWorkbenchFileRef(outputFile.ref, '  name.csv'), null)
  assert.throws(() => rootWorkbenchFileRef('output', ['..']), /segment/)
  assert.throws(() => rootWorkbenchFileRef('input', ['CON']), /segment/)
})

test('T2-L10 artifact and Presentation Skill admission is fail-closed', () => {
  assert.equal(canPublishWorkbenchArtifact(outputFile, 'cmd-1'), true)
  assert.equal(canPublishWorkbenchArtifact({ ...outputFile, type: 'dir' }, 'cmd-1'), false)
  assert.equal(canPublishWorkbenchArtifact({ ...outputFile, ref: rootWorkbenchFileRef('workspace', ['report.csv']) }, 'cmd-1'), false)
  assert.equal(canPublishWorkbenchArtifact(outputFile, ''), false)
  assert.equal(canPublishPresentationCandidate({ ...outputFile, name: 'slides.pptx' }, 'cmd-1'), true)
  assert.equal(canPublishPresentationCandidate(outputFile, 'cmd-1'), false)
})

test('T2-L10 preview mode is explicit and isolated', () => {
  assert.equal(previewSandboxMode('html_active'), 'allow-scripts')
  assert.equal(previewSandboxMode('table_csv'), '')
  assert.equal(previewSandboxMode('presentation_page'), '')
  assert.equal(previewSandboxMode('download_only'), null)
  assert.equal(formatWorkbenchBytes(0), '0 B')
  assert.equal(formatWorkbenchBytes(2048), '2.0 KB')
  const hardened = hardenWorkbenchPreviewHTML('<script>fetch("https://example.invalid")</script>', 'html_active')
  assert.ok(hardened.indexOf('Content-Security-Policy') < hardened.indexOf('<script>'))
  assert.match(hardened, /connect-src 'none'/)
  assert.match(hardened, /object-src 'none'/)
  assert.match(hardened, /script-src 'unsafe-inline'/)
})
