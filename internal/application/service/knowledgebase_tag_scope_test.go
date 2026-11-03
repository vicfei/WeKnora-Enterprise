package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newTagScopeServiceForTest builds a knowledgeBaseService whose kgRepo is a
// real repository over an in-memory database seeded with two document KBs
// (kb-1, kb-2) and one FAQ KB (faq-kb). Tag mapping:
//
//	kb-1: doc-1 ← tag-a, doc-2 ← tag-b, doc-3 ← tag-a + tag-b
//	kb-2: doc-4 ← tag-a
func newTagScopeServiceForTest(t *testing.T) *knowledgeBaseService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.Knowledge{},
		&types.KnowledgeTagRelation{},
		&types.KnowledgeTag{},
	))

	relations := []types.KnowledgeTagRelation{
		{KnowledgeID: "doc-1", TagID: "tag-a", CreatedAt: time.Now()},
		{KnowledgeID: "doc-2", TagID: "tag-b", CreatedAt: time.Now()},
		{KnowledgeID: "doc-3", TagID: "tag-a", CreatedAt: time.Now()},
		{KnowledgeID: "doc-3", TagID: "tag-b", CreatedAt: time.Now()},
		{KnowledgeID: "doc-4", TagID: "tag-a", CreatedAt: time.Now()},
	}
	for _, rel := range relations {
		require.NoError(t, db.Create(&rel).Error)
	}
	for _, docID := range []string{"doc-1", "doc-2", "doc-3"} {
		require.NoError(t, db.Create(&types.Knowledge{
			ID: docID, TenantID: 1, KnowledgeBaseID: "kb-1", Type: types.KnowledgeTypeManual,
			Title: docID, Source: "test",
		}).Error)
	}
	require.NoError(t, db.Create(&types.Knowledge{
		ID: "doc-4", TenantID: 1, KnowledgeBaseID: "kb-2", Type: types.KnowledgeTypeManual,
		Title: "doc-4", Source: "test",
	}).Error)

	return &knowledgeBaseService{kgRepo: repository.NewKnowledgeRepository(db)}
}

func tagScopeKB(id, kbType string) *types.KnowledgeBase {
	return &types.KnowledgeBase{ID: id, TenantID: 1, Type: kbType}
}

func tagScopeContext() context.Context {
	return context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
}

func documentKBs() []*types.KnowledgeBase {
	return []*types.KnowledgeBase{
		tagScopeKB("kb-1", types.KnowledgeBaseTypeDocument),
		tagScopeKB("kb-2", types.KnowledgeBaseTypeDocument),
	}
}

func TestResolveDocumentTagScope_ResolvesKnowledgeWhitelist(t *testing.T) {
	svc := newTagScopeServiceForTest(t)
	params := &types.SearchParams{
		TagIDs:      []string{"tag-a", "tag-b", "tag-a"}, // duplicates are deduped
		ScopeTagIDs: []string{"tag-kept"},
	}

	applied, err := svc.resolveDocumentTagScope(tagScopeContext(), documentKBs(), params)
	require.NoError(t, err)
	assert.True(t, applied)
	// Union across KBs, OR across tags, deduped.
	assert.ElementsMatch(t, []string{"doc-1", "doc-2", "doc-3", "doc-4"}, params.KnowledgeIDs)
	assert.Nil(t, params.TagIDs, "index-level filter must be cleared after resolution")
	// Original tags are preserved for tracing alongside any caller scope.
	assert.ElementsMatch(t, []string{"tag-kept", "tag-a", "tag-b"}, params.ScopeTagIDs)
}

func TestResolveDocumentTagScope_IntersectsExplicitKnowledgeIDs(t *testing.T) {
	svc := newTagScopeServiceForTest(t)
	params := &types.SearchParams{
		TagIDs:       []string{"tag-a"},
		KnowledgeIDs: []string{"doc-2", "doc-3"},
	}

	applied, err := svc.resolveDocumentTagScope(tagScopeContext(), documentKBs(), params)
	require.NoError(t, err)
	assert.True(t, applied)
	assert.ElementsMatch(t, []string{"doc-3"}, params.KnowledgeIDs)
}

func TestResolveDocumentTagScope_EmptyResolutionYieldsEmptyWhitelist(t *testing.T) {
	svc := newTagScopeServiceForTest(t)
	params := &types.SearchParams{TagIDs: []string{"tag-missing"}}

	applied, err := svc.resolveDocumentTagScope(tagScopeContext(), documentKBs(), params)
	require.NoError(t, err)
	assert.True(t, applied)
	assert.Empty(t, params.KnowledgeIDs, "empty resolution must surface as an empty result, not as a dropped filter")
	assert.Nil(t, params.TagIDs)
	assert.ElementsMatch(t, []string{"tag-missing"}, params.ScopeTagIDs)
}

func TestResolveDocumentTagScope_FAQScopeKeepsIndexFilter(t *testing.T) {
	svc := newTagScopeServiceForTest(t)
	params := &types.SearchParams{
		TagIDs:       []string{"tag-a"},
		KnowledgeIDs: []string{"doc-1"},
	}

	applied, err := svc.resolveDocumentTagScope(
		tagScopeContext(),
		[]*types.KnowledgeBase{tagScopeKB("faq-kb", types.KnowledgeBaseTypeFAQ)},
		params,
	)
	require.NoError(t, err)
	assert.False(t, applied)
	assert.Equal(t, []string{"tag-a"}, params.TagIDs, "params must stay untouched for FAQ scopes")
	assert.Equal(t, []string{"doc-1"}, params.KnowledgeIDs)
	assert.Empty(t, params.ScopeTagIDs)
}

func TestResolveDocumentTagScope_MixedScopeKeepsIndexFilter(t *testing.T) {
	svc := newTagScopeServiceForTest(t)
	params := &types.SearchParams{TagIDs: []string{"tag-a"}}

	applied, err := svc.resolveDocumentTagScope(
		tagScopeContext(),
		[]*types.KnowledgeBase{
			tagScopeKB("kb-1", types.KnowledgeBaseTypeDocument),
			tagScopeKB("faq-kb", types.KnowledgeBaseTypeFAQ),
		},
		params,
	)
	require.NoError(t, err)
	assert.False(t, applied)
	assert.Equal(t, []string{"tag-a"}, params.TagIDs, "mixed scopes keep the status-quo passthrough")
	assert.Empty(t, params.KnowledgeIDs)
	assert.Empty(t, params.ScopeTagIDs)
}
