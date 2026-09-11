import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

import {
  CITATION_PROFILE_GUIDANCE,
  createCitationProfileClient,
  isCitationProfileGuidance,
  type CitationProfileTransport,
} from './citationProfile.ts'
import {
  buildNodeStateByUuid,
  CitationProfileRequestFence,
  correctionNeedsRefresh,
  citationProfilePanelState,
  graphCapNotice,
  mergeRenamedNodesByUuid,
  type CitationProfileNodeCard,
} from '../../views/knowledge/wiki/citation-profile/model.ts'

const calls: Array<{ method: string; url: string; body?: unknown; config?: unknown }> = []

const transport: CitationProfileTransport = {
  get: async (url, config) => {
    calls.push({ method: 'get', url, config })
    return { ok: true } as never
  },
  post: async (url, body, config) => {
    calls.push({ method: 'post', url, body, config })
    return { ok: true } as never
  },
  put: async (url, body, config) => {
    calls.push({ method: 'put', url, body, config })
    return { ok: true } as never
  },
  delete: async (url, body, config) => {
    calls.push({ method: 'delete', url, body, config })
    return { ok: true } as never
  },
}

function resetCalls() {
  calls.length = 0
}

test('client uses contract routes and keeps sensitive payloads scoped to the signed-in user', async () => {
  const client = createCitationProfileClient(transport)
  resetCalls()

  await client.getStatus('kb-1')
  await client.listNodes('kb-1', { cursor: 'cursor-1', page_size: 100 })
  await client.getGraph('kb-1')
  await client.getEvidence('kb-1', 'page-uuid-1', { cursor: 'cursor-2', page_size: 100 })
  await client.updateEnrollment('kb-1', {
    enabled: true,
    expected_read_version: '16',
    idempotency_key: 'idem-1',
  })
  await client.createCorrection('kb-1', {
    expected_read_version: '17',
    idempotency_key: 'idem-2',
    action: 'reject_mapping',
    event_id: 'event-1',
    page_uuid: 'page-uuid-1',
    reason_code: 'wrong_page',
  })
  await client.createExport('kb-1', {
    expected_read_version: '17',
    idempotency_key: 'idem-3',
    format: 'json',
  })
  await client.getExport('kb-1', 'operation-1')
  await client.deleteCurrentProfile('kb-1', {
    expected_read_version: '17',
    idempotency_key: 'idem-4',
  })
  await client.blindDeleteScope('kb-1')

  assert.deepEqual(calls.map((call) => [call.method, call.url]), [
    ['get', '/api/v1/knowledgebase/kb-1/citation-profile/status'],
    ['get', '/api/v1/knowledgebase/kb-1/citation-profile/nodes?cursor=cursor-1&page_size=100'],
    ['get', '/api/v1/knowledgebase/kb-1/citation-profile/graph'],
    ['get', '/api/v1/knowledgebase/kb-1/citation-profile/nodes/page-uuid-1/evidence?cursor=cursor-2&page_size=100'],
    ['put', '/api/v1/knowledgebase/kb-1/citation-profile/enrollment'],
    ['post', '/api/v1/knowledgebase/kb-1/citation-profile/corrections'],
    ['post', '/api/v1/knowledgebase/kb-1/citation-profile/exports'],
    ['get', '/api/v1/knowledgebase/kb-1/citation-profile/exports/operation-1'],
    ['delete', '/api/v1/knowledgebase/kb-1/citation-profile/'],
    ['delete', '/api/v1/citation-profile/scopes/kb-1'],
  ])

  const sensitiveCalls = calls.filter((call) => ['put', 'post', 'delete'].includes(call.method))
  for (const call of sensitiveCalls) {
    assert.equal(JSON.stringify(call).includes('"subject'), false)
    assert.equal((call.config as any)?.headers?.['X-WeKnora-CSRF'], '1')
  }
  assert.equal((calls[7].config as any)?.headers?.['X-WeKnora-CSRF'], '1')
})

test('guidance object is exact and unordered list is empty', () => {
  assert.deepEqual(CITATION_PROFILE_GUIDANCE, {
    kind: 'none',
    reason: 'evidence_insufficient',
    candidates: [],
  })
  assert.equal(isCitationProfileGuidance(CITATION_PROFILE_GUIDANCE), true)
  assert.equal(isCitationProfileGuidance({ ...CITATION_PROFILE_GUIDANCE, candidates: ['page-1'] }), false)
})

test('page UUID remains the component identity when display fields change', () => {
  const before = buildNodeStateByUuid<CitationProfileNodeCard>([
    { page_uuid: 'page-a', title: 'Old title', slug: 'old-slug', overlay: 'unknown' },
  ])
  const after = mergeRenamedNodesByUuid(before, [
    { page_uuid: 'page-a', title: 'New title', slug: 'new-slug', overlay: 'evidenced_current' },
  ])

  assert.deepEqual([...after.keys()], ['page-a'])
  assert.equal(after.get('page-a')?.title, 'New title')
  assert.equal(after.get('page-a')?.slug, 'new-slug')
})

test('graph cap notice points users to the complete node list', () => {
  const notice = graphCapNotice({
    graph_truncated: true,
    caps: { max_nodes: 500, max_edges: 2000 },
    complete_list_url: '/api/v1/knowledgebase/kb-1/citation-profile/nodes',
  })

  assert.equal(notice.truncated, true)
  assert.equal(notice.listUrl, '/api/v1/knowledgebase/kb-1/citation-profile/nodes')
  assert.match(notice.text, /500/)
  assert.match(notice.text, /2,000/)
})

test('read version conflict blocks stale correction action until refresh', () => {
  assert.equal(correctionNeedsRefresh('17', '17'), false)
  assert.equal(correctionNeedsRefresh('17', '18'), true)
})

test('deleted tombstone takes precedence over the fail-closed ACL empty state', () => {
  assert.equal(citationProfilePanelState({
    deleted: true,
    enrolled: true,
    suspended: true,
    empty_state: { kind: 'acl_unknown', message_code: 'citation_profile_acl_unknown' },
  }), 'deleted')
})

test('request fence rejects prior-KB and older same-lane responses without cancelling sibling reads', () => {
  const fence = new CitationProfileRequestFence()
  fence.reset('kb-a')
  const aStatus = fence.begin('status')
  const aNodes = fence.begin('nodes')
  const aGraph = fence.begin('graph')

  fence.reset('kb-b')
  const bStatus = fence.begin('status')
  const bNodesFirst = fence.begin('nodes')
  const bGraph = fence.begin('graph')
  const bNodesSecond = fence.begin('nodes')

  assert.equal(fence.isCurrent(aStatus, 'kb-b'), false)
  assert.equal(fence.isCurrent(aNodes, 'kb-b'), false)
  assert.equal(fence.isCurrent(aGraph, 'kb-b'), false)
  assert.equal(fence.isCurrent(bStatus, 'kb-b'), true)
  assert.equal(fence.isCurrent(bGraph, 'kb-b'), true)
  assert.equal(fence.isCurrent(bNodesFirst, 'kb-b'), false)
  assert.equal(fence.isCurrent(bNodesSecond, 'kb-b'), true)

  fence.reset('kb-b')
  assert.equal(fence.isCurrent(bStatus, 'kb-b'), false,
    'a same-KB refresh must fence every response from the previous generation')
})

test('correction continuation validates the loadAll generation before reopening evidence', () => {
  const here = dirname(fileURLToPath(import.meta.url))
  const panel = readFileSync(
    join(here, '../../views/knowledge/wiki/citation-profile/CitationProfilePanel.vue'),
    'utf8',
  )
  assert.match(panel, /const reloadToken = await loadAll\(\)/)
  assert.match(
    panel,
    /requestFence\.isCurrent\(reloadToken, props\.knowledgeBaseId\)[\s\S]{0,180}reopenDrawer/,
    'a superseding same-KB refresh must stop an older correction continuation from reopening the drawer',
  )
})

test('blind delete fences in-flight reads and clears every retained sensitive snapshot', () => {
  const here = dirname(fileURLToPath(import.meta.url))
  const panel = readFileSync(
    join(here, '../../views/knowledge/wiki/citation-profile/CitationProfilePanel.vue'),
    'utf8',
  )
  const blindDeleteBody = panel.match(/async function blindDelete\(\) \{([\s\S]*?)\n\}/)?.[1] || ''

  assert.match(blindDeleteBody, /requestFence\.reset\(knowledgeBaseId\)/)
  assert.match(blindDeleteBody, /resetPanelState\(\)/)
  assert.doesNotMatch(
    blindDeleteBody,
    /loadAll\(/,
    'an existence-hiding delete must not perform a revealing status refresh',
  )

  const resetBody = panel.match(/function resetPanelState\(\) \{([\s\S]*?)\n\}/)?.[1] || ''
  for (const sensitiveRef of ['status', 'nodes', 'nextCursor', 'graph', 'evidence', 'selectedNode']) {
    assert.match(resetBody, new RegExp(`${sensitiveRef}\\.value\\s*=\\s*(?:null|\\[\\])`), sensitiveRef)
  }
})

test('new citation profile files keep prohibited wording out of source', () => {
  const here = dirname(fileURLToPath(import.meta.url))
  const root = join(here, '../../views/knowledge/wiki/citation-profile')
  const files = [
    join(here, 'citationProfile.ts'),
    join(root, 'model.ts'),
    join(root, 'CitationProfilePanel.vue'),
  ]
  const blocked = [
    ['m', 'astery'],
    ['famili', 'arity'],
    ['we', 'akness'],
    ['con', 'fidence'],
    ['r', 'ank'],
    ['pre', 'requisite'],
    ['recom', 'mendation'],
    ['Fam', 'iliar'],
  ].map((parts) => parts.join(''))
  const pattern = new RegExp(`\b(?:${blocked.join('|')})\b`, 'i')
  for (const file of files) {
    assert.doesNotMatch(readFileSync(file, 'utf8'), pattern, file)
  }
})
