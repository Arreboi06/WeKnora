package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ReparseCacheService provides caching for reparse operations to avoid
// recomputing expensive operations like VLM OCR, Embedding, and Wiki map
// when the underlying content hasn't changed.
//
// Cache Strategy:
// - VLM Results: keyed by hash(image_bytes) + model_id + prompt_version
// - Embeddings: keyed by hash(content) + model_id
// - Wiki Map: keyed by hash(content) + granularity + model_id + prompt_version
// - Chunk Hash: keyed by knowledge_id + chunk_index for diff tracking
type ReparseCacheService struct {
	redisClient *redis.Client
	ttl         time.Duration // default TTL for cache entries
}

// NewReparseCacheService creates a new ReparseCacheService.
func NewReparseCacheService(redisClient *redis.Client) *ReparseCacheService {
	return &ReparseCacheService{
		redisClient: redisClient,
		ttl:         30 * 24 * time.Hour, // 30 days default TTL
	}
}

// NewReparseCacheServiceWithTTL creates a new ReparseCacheService with custom TTL.
func NewReparseCacheServiceWithTTL(redisClient *redis.Client, ttl time.Duration) *ReparseCacheService {
	return &ReparseCacheService{
		redisClient: redisClient,
		ttl:         ttl,
	}
}

// CacheKeyPrefix defines the prefixes for different cache types.
const (
	CacheKeyVLMResult    = "vlm:result:"
	CacheKeyEmbedding     = "embed:result:"
	CacheKeyChunkHash     = "chunk:hash:"
	CacheKeyWikiMap       = "wiki:map:"
	CacheKeyParseArtifact = "parse:artifact:"
)

// ContentHash computes a SHA-256 hash of the content.
func ContentHash(content string) string {
	h := sha256.New()
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}

// ImageHash computes a SHA-256 hash of image bytes.
func ImageHash(imageBytes []byte) string {
	h := sha256.New()
	h.Write(imageBytes)
	return hex.EncodeToString(h.Sum(nil))
}

// VLMResultCacheKey generates a cache key for VLM OCR/Caption results.
// Key format: vlm:result:{imageHash}:{modelID}:{promptVersion}
func (s *ReparseCacheService) VLMResultCacheKey(imageHash, modelID, promptVersion string) string {
	return fmt.Sprintf("%s%s:%s:%s", CacheKeyVLMResult, imageHash, modelID, promptVersion)
}

// EmbeddingCacheKey generates a cache key for embedding results.
// Key format: embed:result:{contentHash}:{modelID}
func (s *ReparseCacheService) EmbeddingCacheKey(contentHash, modelID string) string {
	return fmt.Sprintf("%s%s:%s", CacheKeyEmbedding, contentHash, modelID)
}

// ChunkHashCacheKey generates a cache key for chunk content hash.
// Key format: chunk:hash:{knowledgeID}:{chunkIndex}
func (s *ReparseCacheService) ChunkHashCacheKey(knowledgeID string, chunkIndex int) string {
	return fmt.Sprintf("%s%s:%d", CacheKeyChunkHash, knowledgeID, chunkIndex)
}

// WikiMapCacheKey generates a cache key for Wiki per-document map results.
// Key format: wiki:map:{knowledgeID}:{granularity}:{modelID}:{promptVersion}
func (s *ReparseCacheService) WikiMapCacheKey(knowledgeID, granularity, modelID, promptVersion string) string {
	return fmt.Sprintf("%s%s:%s:%s:%s", CacheKeyWikiMap, knowledgeID, granularity, modelID, promptVersion)
}

// ParseArtifactCacheKey generates a cache key for parsed document artifacts.
// Key format: parse:artifact:{fileHash}:{parserVersion}
func (s *ReparseCacheService) ParseArtifactCacheKey(fileHash, parserVersion string) string {
	return fmt.Sprintf("%s%s:%s", CacheKeyParseArtifact, fileHash, parserVersion)
}

// VLMResult represents cached VLM OCR/Caption results.
type VLMResult struct {
	OCRText  string `json:"ocr_text"`
	Caption  string `json:"caption"`
	ModelID  string `json:"model_id"`
	CachedAt int64  `json:"cached_at"`
}

// GetVLMResult retrieves cached VLM OCR/Caption results.
func (s *ReparseCacheService) GetVLMResult(ctx context.Context, imageHash, modelID, promptVersion string) (*VLMResult, error) {
	if s.redisClient == nil {
		return nil, nil
	}
	key := s.VLMResultCacheKey(imageHash, modelID, promptVersion)
	data, err := s.redisClient.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil // cache miss
	}
	if err != nil {
		return nil, err
	}
	var result VLMResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SetVLMResult stores VLM OCR/Caption results in cache.
func (s *ReparseCacheService) SetVLMResult(ctx context.Context, imageHash, modelID, promptVersion string, result *VLMResult) error {
	if s.redisClient == nil {
		return nil
	}
	key := s.VLMResultCacheKey(imageHash, modelID, promptVersion)
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.redisClient.Set(ctx, key, data, s.ttl).Err()
}

// EmbeddingResult represents cached embedding vectors.
type EmbeddingResult struct {
	Vector    []float32 `json:"vector"`
	ModelID   string    `json:"model_id"`
	CachedAt  int64     `json:"cached_at"`
	ExpiresAt int64     `json:"expires_at"`
}

// GetEmbedding retrieves cached embedding vectors.
func (s *ReparseCacheService) GetEmbedding(ctx context.Context, contentHash, modelID string) (*EmbeddingResult, error) {
	if s.redisClient == nil {
		return nil, nil
	}
	key := s.EmbeddingCacheKey(contentHash, modelID)
	data, err := s.redisClient.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil // cache miss
	}
	if err != nil {
		return nil, err
	}
	var result EmbeddingResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SetEmbedding stores embedding vectors in cache.
func (s *ReparseCacheService) SetEmbedding(ctx context.Context, contentHash, modelID string, vector []float32) error {
	if s.redisClient == nil {
		return nil
	}
	key := s.EmbeddingCacheKey(contentHash, modelID)
	result := &EmbeddingResult{
		Vector:   vector,
		ModelID:  modelID,
		CachedAt: time.Now().Unix(),
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.redisClient.Set(ctx, key, data, s.ttl).Err()
}

// GetChunkHash retrieves the stored content hash for a chunk.
func (s *ReparseCacheService) GetChunkHash(ctx context.Context, knowledgeID string, chunkIndex int) (string, error) {
	if s.redisClient == nil {
		return "", nil
	}
	key := s.ChunkHashCacheKey(knowledgeID, chunkIndex)
	hash, err := s.redisClient.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil // cache miss
	}
	return hash, err
}

// SetChunkHash stores the content hash for a chunk.
func (s *ReparseCacheService) SetChunkHash(ctx context.Context, knowledgeID string, chunkIndex int, contentHash string) error {
	if s.redisClient == nil {
		return nil
	}
	key := s.ChunkHashCacheKey(knowledgeID, chunkIndex)
	return s.redisClient.Set(ctx, key, contentHash, s.ttl).Err()
}

// WikiMapResult represents cached Wiki per-document map results.
type WikiMapResult struct {
	Candidates  []string            `json:"candidates"`
	Summary     string              `json:"summary"`
	Classify    map[string][]string `json:"classify"` // slug -> chunk_ids
	ModelID     string              `json:"model_id"`
	CachedAt    int64               `json:"cached_at"`
}

// GetWikiMap retrieves cached Wiki per-document map results.
func (s *ReparseCacheService) GetWikiMap(ctx context.Context, knowledgeID, granularity, modelID, promptVersion string) (*WikiMapResult, error) {
	if s.redisClient == nil {
		return nil, nil
	}
	key := s.WikiMapCacheKey(knowledgeID, granularity, modelID, promptVersion)
	data, err := s.redisClient.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil // cache miss
	}
	if err != nil {
		return nil, err
	}
	var result WikiMapResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SetWikiMap stores Wiki per-document map results in cache.
func (s *ReparseCacheService) SetWikiMap(ctx context.Context, knowledgeID, granularity, modelID, promptVersion string, result *WikiMapResult) error {
	if s.redisClient == nil {
		return nil
	}
	key := s.WikiMapCacheKey(knowledgeID, granularity, modelID, promptVersion)
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.redisClient.Set(ctx, key, data, s.ttl).Err()
}

// InvalidateKnowledgeCache invalidates all caches related to a knowledge item.
// Call this when the knowledge content or configuration changes.
func (s *ReparseCacheService) InvalidateKnowledgeCache(ctx context.Context, knowledgeID string) error {
	if s.redisClient == nil {
		return nil
	}
	// Delete all chunk hash entries for this knowledge
	pattern := fmt.Sprintf("%s%s:*", CacheKeyChunkHash, knowledgeID)
	if err := s.deleteByPattern(ctx, pattern); err != nil {
		return err
	}
	// Delete wiki map entries for this knowledge
	pattern = fmt.Sprintf("%s%s:*", CacheKeyWikiMap, knowledgeID)
	return s.deleteByPattern(ctx, pattern)
}

// InvalidateVLMResult invalidates VLM cache for a specific image.
func (s *ReparseCacheService) InvalidateVLMResult(ctx context.Context, imageHash string) error {
	if s.redisClient == nil {
		return nil
	}
	pattern := fmt.Sprintf("%s%s:*", CacheKeyVLMResult, imageHash)
	return s.deleteByPattern(ctx, pattern)
}

// InvalidateEmbedding invalidates embedding cache for specific content.
func (s *ReparseCacheService) InvalidateEmbedding(ctx context.Context, contentHash string) error {
	if s.redisClient == nil {
		return nil
	}
	pattern := fmt.Sprintf("%s%s:*", CacheKeyEmbedding, contentHash)
	return s.deleteByPattern(ctx, pattern)
}

// InvalidateWikiMap invalidates Wiki map cache for a specific knowledge.
func (s *ReparseCacheService) InvalidateWikiMap(ctx context.Context, knowledgeID string) error {
	if s.redisClient == nil {
		return nil
	}
	pattern := fmt.Sprintf("%s%s:*", CacheKeyWikiMap, knowledgeID)
	return s.deleteByPattern(ctx, pattern)
}

// InvalidateModelCache invalidates all caches that depend on a specific model.
// This is useful when a model configuration changes.
func (s *ReparseCacheService) InvalidateModelCache(ctx context.Context, modelID string, modelType string) error {
	if s.redisClient == nil {
		return nil
	}
	var pattern string
	switch modelType {
	case "vlm":
		pattern = fmt.Sprintf("%s*:%s:*", CacheKeyVLMResult, modelID)
	case "embedding":
		pattern = fmt.Sprintf("%s*:%s", CacheKeyEmbedding, modelID)
	case "chat":
		pattern = fmt.Sprintf("%s*:*:%s:*", CacheKeyWikiMap, modelID)
	default:
		return nil
	}
	return s.deleteByPattern(ctx, pattern)
}

// InvalidatePromptCache invalidates all caches that depend on a specific prompt version.
// This is useful when a prompt template is updated.
func (s *ReparseCacheService) InvalidatePromptCache(ctx context.Context, promptVersion string) error {
	if s.redisClient == nil {
		return nil
	}
	patterns := []string{
		fmt.Sprintf("%s*:*:%s", CacheKeyVLMResult, promptVersion),
		fmt.Sprintf("%s*:*:%s:*", CacheKeyWikiMap, promptVersion),
	}
	for _, pattern := range patterns {
		if err := s.deleteByPattern(ctx, pattern); err != nil {
			return err
		}
	}
	return nil
}

// deleteByPattern deletes all keys matching a pattern.
// Note: This uses KEYS command which is O(N). For large datasets,
// consider using SCAN in production or Redis Cluster with hash slots.
func (s *ReparseCacheService) deleteByPattern(ctx context.Context, pattern string) error {
	var cursor uint64
	for {
		keys, nextCursor, err := s.redisClient.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := s.redisClient.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return nil
}

// ChunkDiff represents the difference between old and new chunks for reparse.
type ChunkDiff struct {
	KnowledgeID   string   `json:"knowledge_id"`
	UnchangedIDs  []string `json:"unchanged_ids"`  // Chunks that can be preserved
	ChangedIDs    []string `json:"changed_ids"`    // Chunks that need to be deleted and recreated
	AddedIndices  []int    `json:"added_indices"` // Indices of new chunks
}

// DiffChunks computes the difference between old chunk hashes and new content hashes.
// Returns chunks that can be preserved vs. those that need to be recreated.
func (s *ReparseCacheService) DiffChunks(ctx context.Context, knowledgeID string, oldChunks map[int]string, newContents map[int]string) (*ChunkDiff, error) {
	diff := &ChunkDiff{
		KnowledgeID:   knowledgeID,
		UnchangedIDs:  []string{},
		ChangedIDs:    []string{},
		AddedIndices:  []int{},
	}

	// Find unchanged chunks (same index and content hash)
	for idx, oldHash := range oldChunks {
		newContent, exists := newContents[idx]
		if !exists {
			// Chunk was removed at this index
			continue
		}
		newHash := ContentHash(newContent)
		if oldHash == newHash {
			// Content unchanged, can preserve
			diff.UnchangedIDs = append(diff.UnchangedIDs, fmt.Sprintf("%s:%d", knowledgeID, idx))
		}
	}

	// Find changed chunks
	for idx := range newContents {
		oldHash, exists := oldChunks[idx]
		if !exists {
			// New chunk at this index
			diff.AddedIndices = append(diff.AddedIndices, idx)
			continue
		}
		newContent := newContents[idx]
		newHash := ContentHash(newContent)
		if oldHash != newHash {
			// Content changed, need to recreate
			diff.ChangedIDs = append(diff.ChangedIDs, fmt.Sprintf("%s:%d", knowledgeID, idx))
		}
	}

	return diff, nil
}
