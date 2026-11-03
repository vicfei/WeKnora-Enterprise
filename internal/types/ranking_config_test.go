package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRankingConfigResolveDefaultsReproduceLegacyConstants(t *testing.T) {
	r := (*RankingConfig)(nil).Resolve()
	assert.Equal(t, LegacyCompositeRerankWeight, r.RerankWeight)
	assert.Equal(t, LegacyCompositeRetrievalWeight, r.RetrievalWeight)
	assert.Equal(t, LegacyCompositeSourceWeight, r.SourceWeight)
	assert.Equal(t, 0.0, r.RecencyWeight)
	assert.Equal(t, DefaultRecencyHalfLifeDays, r.RecencyHalfLifeDays)
	assert.Equal(t, LegacyMMRLambda, r.MMRLambda)

	empty := (&RankingConfig{}).Resolve()
	assert.Equal(t, r, empty, "zero-value config must resolve to the same defaults")
}

func TestRankingConfigResolveAppliesExplicitOverrides(t *testing.T) {
	r := (&RankingConfig{
		RerankWeight:         0.8,
		RecencyWeight:        0.1,
		RecencyHalfLifeDays:  30,
		MMRLambda:            1.0,
	}).Resolve()
	assert.Equal(t, 0.8, r.RerankWeight)
	assert.Equal(t, LegacyCompositeRetrievalWeight, r.RetrievalWeight, "unset field falls back")
	assert.Equal(t, LegacyCompositeSourceWeight, r.SourceWeight)
	assert.Equal(t, 0.1, r.RecencyWeight)
	assert.Equal(t, 30, r.RecencyHalfLifeDays)
	assert.Equal(t, 1.0, r.MMRLambda)
}

func TestRankingConfigMergeFieldWisePrecedence(t *testing.T) {
	base := &RankingConfig{RerankWeight: 0.5, MMRLambda: 0.6}
	merged := base.Merge(&RankingConfig{RerankWeight: 0.9, RecencyWeight: 0.2})
	require.NotNil(t, merged)
	assert.Equal(t, 0.9, merged.RerankWeight, "override wins")
	assert.Equal(t, 0.6, merged.MMRLambda, "unset override keeps base")
	assert.Equal(t, 0.2, merged.RecencyWeight, "override-only field passes through")

	// nil sides are transparent
	assert.Equal(t, base, base.Merge(nil))
	assert.Nil(t, (*RankingConfig)(nil).Merge(nil))
	only := &RankingConfig{MMRLambda: 0.9}
	assert.Equal(t, only, (*RankingConfig)(nil).Merge(only))
}

func TestRetrievalConfigScanNestedRankingRoundTrip(t *testing.T) {
	raw := []byte(`{"rerank_top_k":10,"rrf_k":60,"ranking":{"rerank_weight":0.7,"mmr_lambda":1.0}}`)
	var cfg RetrievalConfig
	require.NoError(t, cfg.Scan(raw))
	ranking := cfg.GetEffectiveRanking()
	require.NotNil(t, ranking)
	assert.Equal(t, 0.7, ranking.RerankWeight)
	assert.Equal(t, 1.0, ranking.MMRLambda)

	out, err := json.Marshal(&cfg)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"ranking":{"rerank_weight":0.7,"mmr_lambda":1}`)

	// Old payloads without the ranking key keep working and resolve to nil.
	var legacy RetrievalConfig
	require.NoError(t, legacy.Scan([]byte(`{"rerank_top_k":5}`)))
	assert.Nil(t, legacy.GetEffectiveRanking())
}
