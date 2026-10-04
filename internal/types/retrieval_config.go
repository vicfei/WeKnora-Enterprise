package types

import (
	"database/sql/driver"
	"encoding/json"
)

// Legacy ranking constants. These were previously hardcoded in
// chat_pipeline/rerank.go; they now live here as the defaults of
// RankingConfig so that enabling weighted ranking without explicit weights
// reproduces the exact legacy scoring behavior.
const (
	LegacyCompositeRerankWeight    = 0.6
	LegacyCompositeRetrievalWeight = 0.3
	LegacyCompositeSourceWeight    = 0.1
	LegacyMMRLambda                = 0.7
	DefaultRecencyHalfLifeDays     = 180
)

// RankingConfig holds the configurable weighted-ranking parameters for the
// rerank composite score (二开特性 "多维度加权排序"). Zero values mean
// "unset" and resolve to the legacy constants above. Stored as an optional
// nested object inside RetrievalConfig (JSONB on the tenants table) and
// overridable per request; the chat entry point merges request-level values
// over the tenant config before the pipeline runs.
type RankingConfig struct {
	// Weight semantics: every weight field independently means "unset → use
	// the legacy default"; zero is not expressible (v1 limitation). The
	// source term is a constant offset per knowledge source, so it cannot
	// reorder candidates that share a source — zeroing it is unnecessary for
	// KB-only candidate sets.
	// RerankWeight applies to the rerank model's relevance score. Default: 0.6.
	RerankWeight float64 `json:"rerank_weight,omitempty"`
	// RetrievalWeight applies to the fused retrieval (RRF) score. Default: 0.3.
	RetrievalWeight float64 `json:"retrieval_weight,omitempty"`
	// SourceWeight applies to the knowledge-source term (a constant offset
	// that discounts web_search hits). Default: 0.1.
	SourceWeight float64 `json:"source_weight,omitempty"`
	// RecencyWeight applies to the document-recency signal. Default: 0 —
	// the signal is plumbed and logged but inert until a corpus with real
	// time spread justifies a weight.
	RecencyWeight float64 `json:"recency_weight,omitempty"`
	// RecencyHalfLifeDays is the half-life of the exponential recency decay:
	// a document updated N days ago scores 2^(-N/RecencyHalfLifeDays).
	// Default: 180.
	RecencyHalfLifeDays int `json:"recency_half_life_days,omitempty"`
	// MMRLambda trades relevance against redundancy in MMR selection
	// (1.0 = pure relevance, no diversity penalty). Default: 0.7.
	MMRLambda float64 `json:"mmr_lambda,omitempty"`
}

// ResolvedRanking is the fully-defaulted form of RankingConfig used by the
// scoring hot path.
type ResolvedRanking struct {
	RerankWeight        float64
	RetrievalWeight     float64
	SourceWeight        float64
	RecencyWeight       float64
	RecencyHalfLifeDays int
	MMRLambda           float64
}

// Resolve returns the effective ranking parameters, filling every unset
// field with its legacy default. Nil-safe.
func (c *RankingConfig) Resolve() ResolvedRanking {
	r := ResolvedRanking{
		RerankWeight:        LegacyCompositeRerankWeight,
		RetrievalWeight:     LegacyCompositeRetrievalWeight,
		SourceWeight:        LegacyCompositeSourceWeight,
		RecencyWeight:       0,
		RecencyHalfLifeDays: DefaultRecencyHalfLifeDays,
		MMRLambda:           LegacyMMRLambda,
	}
	if c == nil {
		return r
	}
	if c.RerankWeight > 0 {
		r.RerankWeight = c.RerankWeight
	}
	if c.RetrievalWeight > 0 {
		r.RetrievalWeight = c.RetrievalWeight
	}
	if c.SourceWeight > 0 {
		r.SourceWeight = c.SourceWeight
	}
	if c.RecencyWeight > 0 {
		r.RecencyWeight = c.RecencyWeight
	}
	if c.RecencyHalfLifeDays > 0 {
		r.RecencyHalfLifeDays = c.RecencyHalfLifeDays
	}
	if c.MMRLambda > 0 {
		r.MMRLambda = c.MMRLambda
	}
	return r
}

// Merge returns a copy of c with every field set in override replacing the
// corresponding field of c. Either side may be nil. The result keeps zero
// values where both sides are unset; callers apply defaults via Resolve.
func (c *RankingConfig) Merge(override *RankingConfig) *RankingConfig {
	if override == nil {
		return c
	}
	if c == nil {
		return override
	}
	merged := *c
	if override.RerankWeight > 0 {
		merged.RerankWeight = override.RerankWeight
	}
	if override.RetrievalWeight > 0 {
		merged.RetrievalWeight = override.RetrievalWeight
	}
	if override.SourceWeight > 0 {
		merged.SourceWeight = override.SourceWeight
	}
	if override.RecencyWeight > 0 {
		merged.RecencyWeight = override.RecencyWeight
	}
	if override.RecencyHalfLifeDays > 0 {
		merged.RecencyHalfLifeDays = override.RecencyHalfLifeDays
	}
	if override.MMRLambda > 0 {
		merged.MMRLambda = override.MMRLambda
	}
	return &merged
}

// RetrievalConfig holds the global retrieval/search configuration for a tenant.
// This replaces the retrieval-related fields previously scattered in ConversationConfig
// and ChatHistoryConfig. Both knowledge search and message search share these parameters.
//
// Stored as a JSONB column on the tenants table, managed via the settings UI
// at /tenants/kv/retrieval-config.
type RetrievalConfig struct {
	// EmbeddingTopK is the maximum number of chunks returned by vector search (default: 50)
	EmbeddingTopK int `json:"embedding_top_k"`
	// VectorThreshold is the minimum vector similarity score (0-1, default: 0.15)
	VectorThreshold float64 `json:"vector_threshold"`
	// KeywordThreshold is the minimum keyword match score (0-1, default: 0.3)
	KeywordThreshold float64 `json:"keyword_threshold"`
	// RerankTopK is the maximum number of results after reranking (default: 10)
	RerankTopK int `json:"rerank_top_k"`
	// RerankThreshold is the minimum rerank score (-10 to 10, default: 0.2)
	RerankThreshold float64 `json:"rerank_threshold"`
	// RerankModelID is the ID of the rerank model to use (required for search)
	RerankModelID string `json:"rerank_model_id"`

	// RRFK is the smoothing constant of Reciprocal Rank Fusion. Larger values
	// flatten the curve, reducing the bias towards top-1 results.
	// Default: 60. Sensible range: 30..100 depending on corpus size.
	RRFK int `json:"rrf_k,omitempty"`
	// RRFVectorWeight is the weight applied to the vector retriever inside RRF.
	// RRFVectorWeight + RRFKeywordWeight should usually sum to 1.0 but the math
	// works for any positive weights. Default: 0.7.
	RRFVectorWeight float64 `json:"rrf_vector_weight,omitempty"`
	// RRFKeywordWeight is the keyword counterpart. Default: 0.3.
	RRFKeywordWeight float64 `json:"rrf_keyword_weight,omitempty"`
	// Ranking is the optional weighted-ranking configuration for the rerank
	// composite score (二开特性, nil = legacy hardcoded behavior).
	Ranking *RankingConfig `json:"ranking,omitempty"`
}

// DefaultRetrievalTopK is the retrieval depth used when a caller supplies no
// usable TopK / MatchCount. HybridSearch also floors its over-retrieval pool
// at this value, so the fallback and the pool it draws from stay in step by
// construction rather than by two independently maintained literals.
const DefaultRetrievalTopK = 50

// MaxRequestedResults caps the result counts a retrieval API caller may ask
// for (knowledge-search match_count, rerank.top_k). knowledge-search runs one
// search per scoped document or tag set and reranks every candidate, so an
// unbounded count multiplied into tens of thousands of billed rerank passages.
const MaxRequestedResults = 200

// GetEffectiveEmbeddingTopK returns EmbeddingTopK with a fallback default.
func (c *RetrievalConfig) GetEffectiveEmbeddingTopK() int {
	if c == nil || c.EmbeddingTopK <= 0 {
		return DefaultRetrievalTopK
	}
	return c.EmbeddingTopK
}

// GetEffectiveVectorThreshold returns VectorThreshold with a fallback default.
func (c *RetrievalConfig) GetEffectiveVectorThreshold() float64 {
	if c == nil || c.VectorThreshold <= 0 {
		return 0.15
	}
	return c.VectorThreshold
}

// GetEffectiveKeywordThreshold returns KeywordThreshold with a fallback default.
func (c *RetrievalConfig) GetEffectiveKeywordThreshold() float64 {
	if c == nil || c.KeywordThreshold <= 0 {
		return 0.3
	}
	return c.KeywordThreshold
}

// GetEffectiveRerankTopK returns RerankTopK with a fallback default.
func (c *RetrievalConfig) GetEffectiveRerankTopK() int {
	if c == nil || c.RerankTopK <= 0 {
		return 10
	}
	return c.RerankTopK
}

// GetEffectiveRerankThreshold returns RerankThreshold with a fallback default.
func (c *RetrievalConfig) GetEffectiveRerankThreshold() float64 {
	if c == nil {
		return 0.2
	}
	return c.RerankThreshold
}

// GetEffectiveRRFK returns the RRF smoothing constant with a fallback default.
func (c *RetrievalConfig) GetEffectiveRRFK() int {
	if c == nil || c.RRFK <= 0 {
		return 60
	}
	return c.RRFK
}

// GetEffectiveRRFWeights returns vector / keyword weights with sensible defaults.
// When neither weight is set explicitly, returns 0.7 / 0.3.
func (c *RetrievalConfig) GetEffectiveRRFWeights() (vector, keyword float64) {
	if c == nil || (c.RRFVectorWeight == 0 && c.RRFKeywordWeight == 0) {
		return 0.7, 0.3
	}
	v := c.RRFVectorWeight
	k := c.RRFKeywordWeight
	if v <= 0 {
		v = 0.7
	}
	if k <= 0 {
		k = 0.3
	}
	return v, k
}

// GetEffectiveRanking returns the tenant-level RankingConfig, or nil when
// the tenant has none configured.
func (c *RetrievalConfig) GetEffectiveRanking() *RankingConfig {
	if c == nil {
		return nil
	}
	return c.Ranking
}

// Value implements the driver.Valuer interface for database serialization
func (c RetrievalConfig) Value() (driver.Value, error) {
	return json.Marshal(c)
}

// Scan implements the sql.Scanner interface for database deserialization
func (c *RetrievalConfig) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		return nil
	}
	return json.Unmarshal(b, c)
}
