import type {
  CitationProfileGraphResponse,
  CitationProfileNode,
  CitationProfileReadVersion,
} from '../../../../api/wiki/citationProfile'

export type CitationProfileNodeCard = Pick<CitationProfileNode, 'page_uuid' | 'title' | 'slug' | 'overlay'>

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