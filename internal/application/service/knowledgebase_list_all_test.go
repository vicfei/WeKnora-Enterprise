package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestListAllKnowledgeBasesExcludesTemporary pins the ?scope=portal admin
// contract: the service returns every KB system-wide except temporary ones.
func TestListAllKnowledgeBasesExcludesTemporary(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.KnowledgeBase{}))

	seed := []*types.KnowledgeBase{
		{ID: "kb-1", Name: "kb-1", TenantID: 1, Type: types.KnowledgeBaseTypeDocument},
		{ID: "kb-2", Name: "kb-2", TenantID: 2, Type: types.KnowledgeBaseTypeDocument},
		{ID: "kb-tmp", Name: "kb-tmp", TenantID: 2, Type: types.KnowledgeBaseTypeDocument, IsTemporary: true},
	}
	for _, kb := range seed {
		require.NoError(t, db.Create(kb).Error)
	}

	svc := &knowledgeBaseService{repo: repository.NewKnowledgeBaseRepository(db)}
	kbs, err := svc.ListAllKnowledgeBases(context.Background())
	require.NoError(t, err)
	require.Len(t, kbs, 2, "temporary KBs must be excluded")
	ids := []string{kbs[0].ID, kbs[1].ID}
	assert.ElementsMatch(t, []string{"kb-1", "kb-2"}, ids)
}
