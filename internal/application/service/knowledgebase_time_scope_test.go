package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newTimeScopeServiceForTest seeds one document KB (kb-1) and one FAQ KB
// (faq-kb) with knowledge rows spread over a business-date timeline:
//
//	kb-1:   doc-old (2025-01), doc-mid (2026-03), doc-new (2026-09),
//	        doc-unknown (NULL business_date)
//	faq-kb: faq-1 (updated_at 2026-08)
func newTimeScopeServiceForTest(t *testing.T) *knowledgeBaseService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Knowledge{}))

	mustTime := func(s string) *time.Time {
		tt, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("bad seed time %q: %v", s, err)
		}
		return &tt
	}
	rows := []types.Knowledge{
		{ID: "doc-old", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual, Title: "old", Source: "test", BusinessDate: mustTime("2025-01-15T00:00:00Z")},
		{ID: "doc-mid", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual, Title: "mid", Source: "test", BusinessDate: mustTime("2026-03-10T00:00:00Z")},
		{ID: "doc-new", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual, Title: "new", Source: "test", BusinessDate: mustTime("2026-09-01T00:00:00Z")},
		{ID: "doc-unknown", TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual, Title: "unknown", Source: "test"},
		{ID: "faq-1", TenantID: 1, KnowledgeBaseID: "faq-kb", Type: types.KnowledgeTypeFAQ, Title: "faq", Source: "test", UpdatedAt: mustTime("2026-08-20T00:00:00Z").UTC()},
	}
	for _, row := range rows {
		require.NoError(t, db.Create(&row).Error)
	}
	return &knowledgeBaseService{kgRepo: repository.NewKnowledgeRepository(db)}
}

func timeWindow(start, end string) *types.SearchTimeCondition {
	s, err := time.Parse(time.RFC3339, start)
	if err != nil {
		panic(err)
	}
	tc := &types.SearchTimeCondition{Field: types.SearchTimeFieldBusinessDate, Op: types.SearchTimeOpBetween, Start: s}
	if end != "" {
		e, err := time.Parse(time.RFC3339, end)
		if err != nil {
			panic(err)
		}
		tc.End = e
	}
	return tc
}

func TestResolveDocumentTimeScope_BetweenWindow(t *testing.T) {
	svc := newTimeScopeServiceForTest(t)
	params := &types.SearchParams{TimeCondition: timeWindow("2026-01-01T00:00:00Z", "2026-12-31T00:00:00Z")}

	applied, err := svc.resolveDocumentTimeScope(tagScopeContext(), documentKBs(), params)
	require.NoError(t, err)
	assert.True(t, applied)
	// NULL business_date never matches; only in-window docs do.
	assert.ElementsMatch(t, []string{"doc-mid", "doc-new"}, params.KnowledgeIDs)
}

func TestResolveDocumentTimeScope_GteOpenEnded(t *testing.T) {
	svc := newTimeScopeServiceForTest(t)
	params := &types.SearchParams{TimeCondition: &types.SearchTimeCondition{
		Field: types.SearchTimeFieldBusinessDate, Op: types.SearchTimeOpGte,
		Start: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}}

	applied, err := svc.resolveDocumentTimeScope(tagScopeContext(), documentKBs(), params)
	require.NoError(t, err)
	assert.True(t, applied)
	assert.ElementsMatch(t, []string{"doc-new"}, params.KnowledgeIDs)
}

func TestResolveDocumentTimeScope_EmptyWindowYieldsEmptyWhitelist(t *testing.T) {
	svc := newTimeScopeServiceForTest(t)
	params := &types.SearchParams{TimeCondition: timeWindow("2027-01-01T00:00:00Z", "2027-12-31T00:00:00Z")}

	applied, err := svc.resolveDocumentTimeScope(tagScopeContext(), documentKBs(), params)
	require.NoError(t, err)
	assert.True(t, applied)
	assert.Empty(t, params.KnowledgeIDs, "empty window = empty whitelist, never whole-KB fallback")
}

func TestResolveDocumentTimeScope_IntersectsExistingWhitelist(t *testing.T) {
	svc := newTimeScopeServiceForTest(t)
	params := &types.SearchParams{
		TimeCondition: timeWindow("2026-01-01T00:00:00Z", "2026-12-31T00:00:00Z"),
		KnowledgeIDs:  []string{"doc-new", "doc-unknown"},
	}

	applied, err := svc.resolveDocumentTimeScope(tagScopeContext(), documentKBs(), params)
	require.NoError(t, err)
	assert.True(t, applied)
	// doc-unknown was explicitly requested but carries no business_date:
	// the time window wins the intersection.
	assert.ElementsMatch(t, []string{"doc-new"}, params.KnowledgeIDs)
}

func TestResolveDocumentTimeScope_FAQFallsBackToUpdatedAt(t *testing.T) {
	svc := newTimeScopeServiceForTest(t)
	params := &types.SearchParams{TimeCondition: timeWindow("2026-07-01T00:00:00Z", "2026-12-31T00:00:00Z")}
	mixed := []*types.KnowledgeBase{
		tagScopeKB("kb-1", types.KnowledgeBaseTypeDocument),
		tagScopeKB("faq-kb", types.KnowledgeBaseTypeFAQ),
	}

	applied, err := svc.resolveDocumentTimeScope(tagScopeContext(), mixed, params)
	require.NoError(t, err)
	assert.True(t, applied)
	// Whole scope degrades to updated_at: docs land via their gorm-managed
	// timestamps, the FAQ entry via its seeded updated_at.
	assert.Contains(t, params.KnowledgeIDs, "faq-1")
}

func TestResolveDocumentTimeScope_NoConditionOrPreferIsNoop(t *testing.T) {
	svc := newTimeScopeServiceForTest(t)

	params := &types.SearchParams{}
	applied, err := svc.resolveDocumentTimeScope(tagScopeContext(), documentKBs(), params)
	require.NoError(t, err)
	assert.False(t, applied)
	assert.Nil(t, params.KnowledgeIDs)

	prefer := &types.SearchParams{TimeCondition: &types.SearchTimeCondition{
		Field: types.SearchTimeFieldBusinessDate, Op: types.SearchTimeOpGte,
		Start: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Strictness: types.SearchTimeStrictnessPrefer,
	}}
	applied, err = svc.resolveDocumentTimeScope(tagScopeContext(), documentKBs(), prefer)
	require.NoError(t, err)
	assert.False(t, applied, "prefer strictness must not hard-filter")
	assert.Nil(t, prefer.KnowledgeIDs)
}

func TestResolveDocumentTimeScope_UnknownOpIsIgnored(t *testing.T) {
	svc := newTimeScopeServiceForTest(t)
	params := &types.SearchParams{TimeCondition: &types.SearchTimeCondition{
		Field: types.SearchTimeFieldBusinessDate, Op: "approx",
		Start: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}}

	applied, err := svc.resolveDocumentTimeScope(tagScopeContext(), documentKBs(), params)
	require.NoError(t, err)
	assert.False(t, applied, "unknown operator: no condition rather than a wrong one")
	assert.Nil(t, params.KnowledgeIDs)
}
