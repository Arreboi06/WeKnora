package types

// ReparseCacheConfig holds configuration for reparse caching behavior.
type ReparseCacheConfig struct {
	// EnableVLMCache enables VLM OCR/Caption result caching.
	EnableVLMCache bool `json:"enable_vlm_cache"`
	// EnableEmbeddingCache enables embedding result caching.
	EnableEmbeddingCache bool `json:"enable_embedding_cache"`
	// EnableWikiMapCache enables Wiki per-document map result caching.
	EnableWikiMapCache bool `json:"enable_wiki_map_cache"`
	// EnableIncrementalReparse enables incremental reparse (only reprocess changed chunks).
	EnableIncrementalReparse bool `json:"enable_incremental_reparse"`
	// CacheTTL is the default time-to-live for cache entries in seconds.
	CacheTTL int64 `json:"cache_ttl"`
}

// DefaultReparseCacheConfig returns the default cache configuration.
func DefaultReparseCacheConfig() *ReparseCacheConfig {
	return &ReparseCacheConfig{
		EnableVLMCache:           true,
		EnableEmbeddingCache:     true,
		EnableWikiMapCache:       true,
		EnableIncrementalReparse: true,
		CacheTTL:                 2592000, // 30 days in seconds
	}
}

// ReparseOptions defines options for reparse operations.
type ReparseOptions struct {
	// SkipCache forces a full recompute, ignoring all cached results.
	SkipCache bool `json:"skip_cache"`
	// SkipVLMCache skips VLM result caching for this operation.
	SkipVLMCache bool `json:"skip_vlm_cache"`
	// SkipEmbeddingCache skips embedding result caching for this operation.
	SkipEmbeddingCache bool `json:"skip_embedding_cache"`
	// SkipWikiMapCache skips Wiki map result caching for this operation.
	SkipWikiMapCache bool `json:"skip_wiki_map_cache"`
	// ForceReprocessChunks forces reprocessing of specified chunk indices.
	// If nil or empty, uses content hash comparison to determine what to reprocess.
	ForceReprocessChunks []int `json:"force_reprocess_chunks"`
}

// CacheStats holds statistics about cache operations.
type CacheStats struct {
	// VLM cache stats
	VLMHits   int64 `json:"vlm_hits"`
	VLMMisses int64 `json:"vlm_misses"`
	// Embedding cache stats
	EmbeddingHits   int64 `json:"embedding_hits"`
	EmbeddingMisses int64 `json:"embedding_misses"`
	// Wiki map cache stats
	WikiMapHits   int64 `json:"wiki_map_hits"`
	WikiMapMisses int64 `json:"wiki_map_misses"`
	// Incremental reparse stats
	ChunksPreserved int64 `json:"chunks_preserved"`
	ChunksRecreated int64 `json:"chunks_recreated"`
}

// PromptVersion constants for cache invalidation.
// These should be updated when prompt templates change.
const (
	PromptVersionOCR     = "v1.0"
	PromptVersionCaption  = "v1.0"
	PromptVersionSummary  = "v1.0"
	PromptVersionQuestion = "v1.0"
	PromptVersionWikiExtract   = "v1.0"
	PromptVersionWikiDedup     = "v1.0"
	PromptVersionWikiSummary  = "v1.0"
	PromptVersionWikiClassify = "v1.0"
)

// ParserVersion constants for cache invalidation.
// These should be updated when parsing logic changes.
const (
	ParserVersionMarkdown = "v1.0"
	ParserVersionPDF     = "v1.0"
	ParserVersionHTML    = "v1.0"
	ParserVersionOffice  = "v1.0"
)
