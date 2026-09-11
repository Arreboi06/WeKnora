import type {
  CitationProfileGraphResponse,
  CitationProfileNode,
  CitationProfileReadVersion,
  CitationProfileStatusResponse,
} from '../../../../api/wiki/citationProfile'

export type CitationProfileNodeCard = Pick<CitationProfileNode, 'page_uuid' | 'title' | 'slug' | 'overlay'>
export type CitationProfilePanelState = 'deleted' | 'feature_disabled' | 'acl_unknown' | 'not_enrolled' | 'ready'

export interface CitationProfileRequestToken {
  readonly generation: number
  readonly knowledgeBaseId: string
  readonly lane: string
  readonly sequence: number
}

/**
 * Fences asynchronous panel work by both KB generation and request lane.
 * Navigating or refreshing advances the generation; starting newer work in one
 * lane supersedes only that lane, so status, graph, and node fan-out may still
 * complete independently without allowing an older response to overwrite it.
 */
export class CitationProfileRequestFence {
  private generation = 0
  private knowledgeBaseId = ''
  private readonly laneSequences = new Map<string, number>()

  reset(knowledgeBaseId: string): number {
    this.generation += 1
    this.knowledgeBaseId = knowledgeBaseId
    this.laneSequences.clear()
    return this.generation
  }

  begin(lane: string): CitationProfileRequestToken {
    const sequence = (this.laneSequences.get(lane) || 0) + 1
    this.laneSequences.set(lane, sequence)
    return {
      generation: this.generation,
      knowledgeBaseId: this.knowledgeBaseId,
      lane,
      sequence,
    }
  }

  isCurrent(token: CitationProfileRequestToken, knowledgeBaseId: string): boolean {
    return token.generation === this.generation
      && token.knowledgeBaseId === this.knowledgeBaseId
      && token.knowledgeBaseId === knowledgeBaseId
      && this.laneSequences.get(token.lane) === token.sequence
  }
}

export function citationProfilePanelState(
  status: Pick<CitationProfileStatusResponse, 'deleted' | 'enrolled' | 'suspended' | 'empty_state'>,
): CitationProfilePanelState {
  // A deleted scope is deliberately returned as a redacted 200 tombstone so
  // the owner can see the deletion receipt and re-enroll. It must win over
  // acl_unknown, which is also present because deleted scopes are fenced.
  if (status.deleted) return 'deleted'
  if (status.empty_state?.kind === 'feature_disabled') return 'feature_disabled'
  if (status.empty_state?.kind === 'acl_unknown' || status.suspended) return 'acl_unknown'
  if (!status.enrolled) return 'not_enrolled'
  return 'ready'
}

export function citationProfileNodeKey(node: Pick<CitationProfileNode, 'page_uuid'>): string {
  return node.page_uuid
}

export function buildNodeStateByUuid<T extends CitationProfileNodeCard>(nodes: T[]): Map<string, T> {
  return new Map(nodes.map((node) => [citationProfileNodeKey(node), node]))
}

export function mergeRenamedNodesByUuid<T extends CitationProfileNodeCard>(
  previous: Map<string, T>,
  nextNodes: T[],
): Map<string, T> {
  const next = new Map(previous)
  for (const node of nextNodes) {
    next.set(node.page_uuid, {
      ...(next.get(node.page_uuid) || node),
      ...node,
    })
  }
  return next
}

export function graphCapNotice(
  graph: Pick<CitationProfileGraphResponse, 'graph_truncated' | 'caps' | 'complete_list_url'>,
): { truncated: boolean; text: string; listUrl: string } {
  const maxNodes = graph.caps.max_nodes.toLocaleString()
  const maxEdges = graph.caps.max_edges.toLocaleString()
  return {
    truncated: graph.graph_truncated,
    text: graph.graph_truncated
      ? `图谱已限制为 ${maxNodes} 个节点和 ${maxEdges} 条关系；完整证据请查看节点列表。`
      : `图谱在 ${maxNodes} 个节点和 ${maxEdges} 条关系限制内。`,
    listUrl: graph.complete_list_url,
  }
}

export function correctionNeedsRefresh(
  actionReadVersion: CitationProfileReadVersion | null | undefined,
  currentReadVersion: CitationProfileReadVersion | null | undefined,
): boolean {
  return !!actionReadVersion && !!currentReadVersion && actionReadVersion !== currentReadVersion
}

export function makeCitationProfileIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID()
  return `00000000-0000-4000-8000-${Date.now().toString(16).padStart(12, '0').slice(-12)}`
}
