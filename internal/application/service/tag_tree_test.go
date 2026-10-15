package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type stubTagTreeKBService struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s *stubTagTreeKBService) GetKnowledgeBaseByID(ctx context.Context, id string) (*types.KnowledgeBase, error) {
	if s.kb == nil || s.kb.ID != id {
		return nil, werrors.NewNotFoundError("知识库不存在")
	}
	return s.kb, nil
}

type stubTagTreeKBShareService struct {
	interfaces.KBShareService
	allowed bool
}

func (s *stubTagTreeKBShareService) HasTenantKBPermission(
	ctx context.Context,
	kbID string,
	callerTenantID uint64,
	callerTenantRole types.TenantRole,
	requiredRole types.OrgMemberRole,
) (bool, error) {
	return s.allowed, nil
}

func newTagTreeServiceForTest(t *testing.T) (interfaces.TagTreeService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.TagTree{},
		&types.KnowledgeTag{},
		&types.KnowledgeTagRelation{},
		&types.Chunk{},
	))
	svc, err := NewTagTreeService(
		&stubTagTreeKBService{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 1}},
		// allowed=false keeps cross-tenant requests forbidden; owning-tenant
		// requests never reach the share check.
		&stubTagTreeKBShareService{allowed: false},
		repository.NewTagTreeRepository(db),
		repository.NewKnowledgeTagRepository(db),
	)
	require.NoError(t, err)
	return svc, db
}

func tagTreeTestCtx() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	return context.WithValue(ctx, types.UserIDContextKey, "user-1")
}

func strPtrForTagTree(s string) *string { return &s }

func intPtrForTagTree(i int) *int { return &i }

func requireAppErrorCode(t *testing.T, err error, code werrors.ErrorCode) {
	t.Helper()
	appErr, ok := werrors.IsAppError(err)
	require.True(t, ok, "expected AppError, got %v", err)
	assert.Equal(t, code, appErr.Code)
}

func TestTagTreeServiceCreateRootNode(t *testing.T) {
	svc, db := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	node, err := svc.CreateNode(ctx, "kb-1", nil, " 行业研究 ", 3)
	require.NoError(t, err)
	assert.Equal(t, "行业研究", node.Name)
	assert.Equal(t, "/行业研究", node.Path)
	assert.Equal(t, 0, node.Level)
	assert.True(t, node.IsLeaf)
	require.NotNil(t, node.TagID)
	assert.Equal(t, 3, node.SortOrder)

	// The leaf's native tag is created automatically.
	var tag types.KnowledgeTag
	require.NoError(t, db.Where("id = ?", *node.TagID).First(&tag).Error)
	assert.Equal(t, "行业研究", tag.Name)
}

func TestTagTreeServiceCreateChildKeepsParentTag(t *testing.T) {
	svc, db := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	root, err := svc.CreateNode(ctx, "kb-1", nil, "新能源", 0)
	require.NoError(t, err)
	require.NotNil(t, root.TagID)
	require.NoError(t, db.Create(&types.Chunk{
		ID: "chunk-1", TenantID: 1, KnowledgeBaseID: "kb-1", KnowledgeID: "k-1",
		Content: "faq entry", ChunkIndex: 0, TagID: *root.TagID,
	}).Error)
	require.NoError(t, db.Create(&types.KnowledgeTagRelation{KnowledgeID: "k-1", TagID: *root.TagID}).Error)

	child, err := svc.CreateNode(ctx, "kb-1", &root.ID, "锂电", 0)
	require.NoError(t, err)
	assert.Equal(t, "/新能源/锂电", child.Path)
	assert.Equal(t, 1, child.Level)
	assert.True(t, child.IsLeaf)

	// 标签树 2.0:建子节点不再降级父级——目录保留自己的原生标签,
	// 既有打标关系与 FAQ 分块标签全部原样保留(选谁打谁)。
	tree, err := svc.GetTree(ctx, "kb-1")
	require.NoError(t, err)
	require.Len(t, tree, 1)
	assert.True(t, tree[0].IsLeaf, "目录携带标签,IsLeaf=可打标而非无子节点")
	require.NotNil(t, tree[0].TagID)
	require.Equal(t, *root.TagID, *tree[0].TagID)
	require.Len(t, tree[0].Children, 1)
	assert.Equal(t, "锂电", tree[0].Children[0].Name)

	var tagCount, relCount int64
	db.Model(&types.KnowledgeTag{}).Where("id = ?", *root.TagID).Count(&tagCount)
	assert.EqualValues(t, 1, tagCount)
	db.Model(&types.KnowledgeTagRelation{}).Where("tag_id = ?", *root.TagID).Count(&relCount)
	assert.EqualValues(t, 1, relCount)
	var chunk types.Chunk
	require.NoError(t, db.Where("id = ?", "chunk-1").First(&chunk).Error)
	assert.Equal(t, *root.TagID, chunk.TagID)
}

func TestTagTreeServiceCreateValidations(t *testing.T) {
	svc, _ := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	_, err := svc.CreateNode(ctx, "kb-1", nil, types.UntaggedTagName, 0)
	requireAppErrorCode(t, err, werrors.ErrBadRequest)

	_, err = svc.CreateNode(ctx, "kb-1", nil, "新能源/锂电", 0)
	requireAppErrorCode(t, err, werrors.ErrBadRequest)

	_, err = svc.CreateNode(ctx, "kb-1", nil, "  ", 0)
	requireAppErrorCode(t, err, werrors.ErrBadRequest)

	_, err = svc.CreateNode(ctx, "missing-kb", nil, "行业研究", 0)
	require.Error(t, err)

	_, err = svc.CreateNode(ctx, "kb-1", strPtrForTagTree("no-such-parent"), "行业研究", 0)
	requireAppErrorCode(t, err, werrors.ErrNotFound)
}

func TestTagTreeServiceCreateSiblingNameConflict(t *testing.T) {
	svc, _ := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	_, err := svc.CreateNode(ctx, "kb-1", nil, "新能源", 0)
	require.NoError(t, err)
	_, err = svc.CreateNode(ctx, "kb-1", nil, "新能源", 0)
	requireAppErrorCode(t, err, werrors.ErrConflict)

	// The same name under a different parent is allowed.
	root2, err := svc.CreateNode(ctx, "kb-1", nil, "行业研究", 0)
	require.NoError(t, err)
	_, err = svc.CreateNode(ctx, "kb-1", &root2.ID, "新能源", 0)
	require.NoError(t, err)
}

func TestTagTreeServiceDepthLimit(t *testing.T) {
	svc, _ := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	parent, err := svc.CreateNode(ctx, "kb-1", nil, "L0", 0)
	require.NoError(t, err)
	for i := 1; i <= MaxTagTreeDepth; i++ {
		parent, err = svc.CreateNode(ctx, "kb-1", &parent.ID, fmt.Sprintf("L%d", i), 0)
		require.NoError(t, err, "level %d must be allowed", i)
	}
	_, err = svc.CreateNode(ctx, "kb-1", &parent.ID, "TooDeep", 0)
	requireAppErrorCode(t, err, werrors.ErrBadRequest)
}

func TestTagTreeServiceUpdateRenameRecomputesSubtree(t *testing.T) {
	svc, _ := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	root, err := svc.CreateNode(ctx, "kb-1", nil, "新能源", 0)
	require.NoError(t, err)
	child, err := svc.CreateNode(ctx, "kb-1", &root.ID, "锂电", 0)
	require.NoError(t, err)
	grandchild, err := svc.CreateNode(ctx, "kb-1", &child.ID, "隔膜", 0)
	require.NoError(t, err)

	updated, err := svc.UpdateNode(ctx, "kb-1", root.ID, strPtrForTagTree("新能源车"), nil)
	require.NoError(t, err)
	assert.Equal(t, "/新能源车", updated.Path)

	// Descendant paths are recomputed.
	childAfter, err := svc.GetNode(ctx, "kb-1", child.ID)
	require.NoError(t, err)
	assert.Equal(t, "/新能源车/锂电", childAfter.Path)
	grandchildAfter, err := svc.GetNode(ctx, "kb-1", grandchild.ID)
	require.NoError(t, err)
	assert.Equal(t, "/新能源车/锂电/隔膜", grandchildAfter.Path)

	// Renaming a leaf syncs its native tag name.
	leafRenamed, err := svc.UpdateNode(ctx, "kb-1", grandchild.ID, strPtrForTagTree("隔膜材料"), nil)
	require.NoError(t, err)
	assert.Equal(t, "/新能源车/锂电/隔膜材料", leafRenamed.Path)
}

func TestTagTreeServiceUpdateRenameConflict(t *testing.T) {
	svc, _ := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	a, err := svc.CreateNode(ctx, "kb-1", nil, "甲", 0)
	require.NoError(t, err)
	b, err := svc.CreateNode(ctx, "kb-1", nil, "乙", 0)
	require.NoError(t, err)

	_, err = svc.UpdateNode(ctx, "kb-1", b.ID, strPtrForTagTree("甲"), nil)
	requireAppErrorCode(t, err, werrors.ErrConflict)

	// Same node rename to its own name is a no-op, not a conflict.
	same, err := svc.UpdateNode(ctx, "kb-1", a.ID, strPtrForTagTree("甲"), nil)
	require.NoError(t, err)
	assert.Equal(t, "甲", same.Name)

	// Sort-order-only update.
	sorted, err := svc.UpdateNode(ctx, "kb-1", a.ID, nil, intPtrForTagTree(7))
	require.NoError(t, err)
	assert.Equal(t, 7, sorted.SortOrder)
}

func TestTagTreeServiceMoveRejectsCycles(t *testing.T) {
	svc, _ := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	root, err := svc.CreateNode(ctx, "kb-1", nil, "行业", 0)
	require.NoError(t, err)
	child, err := svc.CreateNode(ctx, "kb-1", &root.ID, "半导体", 0)
	require.NoError(t, err)
	_, err = svc.CreateNode(ctx, "kb-1", &child.ID, "设计", 0)
	require.NoError(t, err)

	_, err = svc.MoveNode(ctx, "kb-1", root.ID, root.ID)
	requireAppErrorCode(t, err, werrors.ErrBadRequest)

	grandchild, err := svc.ListNodes(ctx, "kb-1", &child.ID)
	require.NoError(t, err)
	require.Len(t, grandchild, 1)
	_, err = svc.MoveNode(ctx, "kb-1", root.ID, grandchild[0].ID)
	requireAppErrorCode(t, err, werrors.ErrBadRequest)
}

func TestTagTreeServiceMoveRecomputesSubtreeAndKeepsLeafTarget(t *testing.T) {
	svc, _ := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	a, err := svc.CreateNode(ctx, "kb-1", nil, "A", 0)
	require.NoError(t, err)
	a1, err := svc.CreateNode(ctx, "kb-1", &a.ID, "A1", 0)
	require.NoError(t, err)
	_, err = svc.CreateNode(ctx, "kb-1", &a1.ID, "A1a", 0)
	require.NoError(t, err)
	b, err := svc.CreateNode(ctx, "kb-1", nil, "B", 0) // leaf target
	require.NoError(t, err)
	require.True(t, b.IsLeaf)

	moved, err := svc.MoveNode(ctx, "kb-1", a1.ID, b.ID)
	require.NoError(t, err)
	assert.Equal(t, "/B/A1", moved.Path)
	assert.Equal(t, 1, moved.Level)
	require.NotNil(t, moved.ParentID)
	assert.Equal(t, b.ID, *moved.ParentID)

	// The moved subtree's descendant follows.
	a1Children, err := svc.ListNodes(ctx, "kb-1", &a1.ID)
	require.NoError(t, err)
	require.Len(t, a1Children, 1)
	assert.Equal(t, "/B/A1/A1a", a1Children[0].Path)
	assert.Equal(t, 2, a1Children[0].Level)

	// 标签树 2.0:移入叶子目标不再降级——目标保留自己的标签。
	bAfter, err := svc.GetNode(ctx, "kb-1", b.ID)
	require.NoError(t, err)
	assert.True(t, bAfter.IsLeaf)
	require.NotNil(t, bAfter.TagID)

	// Moving to the current parent is a no-op.
	noOp, err := svc.MoveNode(ctx, "kb-1", a1.ID, b.ID)
	require.NoError(t, err)
	assert.Equal(t, moved.Path, noOp.Path)
}

func TestTagTreeServiceMoveToRoot(t *testing.T) {
	svc, _ := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	root, err := svc.CreateNode(ctx, "kb-1", nil, "行业", 0)
	require.NoError(t, err)
	child, err := svc.CreateNode(ctx, "kb-1", &root.ID, "半导体", 0)
	require.NoError(t, err)

	moved, err := svc.MoveNode(ctx, "kb-1", child.ID, "")
	require.NoError(t, err)
	assert.Equal(t, "/半导体", moved.Path)
	assert.Equal(t, 0, moved.Level)
	assert.Nil(t, moved.ParentID)
}

func TestTagTreeServiceDeleteNodeCascades(t *testing.T) {
	svc, db := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	root, err := svc.CreateNode(ctx, "kb-1", nil, "行业", 0)
	require.NoError(t, err)
	leafA, err := svc.CreateNode(ctx, "kb-1", &root.ID, "半导体", 0)
	require.NoError(t, err)
	leafB, err := svc.CreateNode(ctx, "kb-1", &root.ID, "新能源", 0)
	require.NoError(t, err)
	require.NoError(t, db.Create(&types.Chunk{
		ID: "chunk-a", TenantID: 1, KnowledgeBaseID: "kb-1", KnowledgeID: "k-1",
		Content: "faq a", ChunkIndex: 0, TagID: *leafA.TagID,
	}).Error)
	require.NoError(t, db.Create(&types.Chunk{
		ID: "chunk-b", TenantID: 1, KnowledgeBaseID: "kb-1", KnowledgeID: "k-2",
		Content: "faq b", ChunkIndex: 0, TagID: *leafB.TagID,
	}).Error)
	require.NoError(t, db.Create(&types.KnowledgeTagRelation{KnowledgeID: "k-1", TagID: *leafA.TagID}).Error)
	require.NoError(t, db.Create(&types.KnowledgeTagRelation{KnowledgeID: "k-2", TagID: *leafB.TagID}).Error)

	result, err := svc.DeleteNode(ctx, "kb-1", root.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 3, result.DeletedNodes)
	// 标签树 2.0:目录也携带标签,级联删除 3 个原生标签(行业+半导体+新能源)。
	assert.EqualValues(t, 3, result.DeletedTags)
	assert.EqualValues(t, 2, result.UntaggedChunks)
	assert.EqualValues(t, 2, result.RemovedRelations)

	// Tree empty, tags gone, chunks survive untagged.
	tree, err := svc.GetTree(ctx, "kb-1")
	require.NoError(t, err)
	assert.Empty(t, tree)
	var chunks []types.Chunk
	require.NoError(t, db.Find(&chunks).Error)
	require.Len(t, chunks, 2)
	assert.Equal(t, "", chunks[0].TagID)
	assert.Equal(t, "", chunks[1].TagID)

	// Deleting a missing node reports not-found.
	_, err = svc.DeleteNode(ctx, "kb-1", root.ID)
	requireAppErrorCode(t, err, werrors.ErrNotFound)
}

func TestTagTreeServiceGetLeaves(t *testing.T) {
	svc, _ := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	root, err := svc.CreateNode(ctx, "kb-1", nil, "行业", 0)
	require.NoError(t, err)
	leafA, err := svc.CreateNode(ctx, "kb-1", &root.ID, "半导体", 0)
	require.NoError(t, err)
	leafB, err := svc.CreateNode(ctx, "kb-1", &root.ID, "新能源", 0)
	require.NoError(t, err)

	// 标签树 2.0(B 语义):目录解析为自身标签 + 子树标签的并集。
	leaves, err := svc.GetLeaves(ctx, "kb-1", root.ID)
	require.NoError(t, err)
	// 树 2.0:IsLeaf 表示携带标签(可打标),与是否有子节点无关。
	assert.True(t, leaves.IsLeaf)
	assert.Equal(t, []string{*root.TagID, *leafA.TagID, *leafB.TagID}, leaves.TagIDs)
	assert.Equal(t, 3, leaves.Count)

	// Leaf node resolves to its own tag.
	leaves, err = svc.GetLeaves(ctx, "kb-1", leafA.ID)
	require.NoError(t, err)
	assert.True(t, leaves.IsLeaf)
	assert.Equal(t, []string{*leafA.TagID}, leaves.TagIDs)

	// 标签树 2.0:目录自带标签,唯一子节点删除后节点仍解析到自己的标签。
	orphan, err := svc.CreateNode(ctx, "kb-1", nil, "临时", 0)
	require.NoError(t, err)
	onlyChild, err := svc.CreateNode(ctx, "kb-1", &orphan.ID, "子类", 0)
	require.NoError(t, err)
	_, err = svc.DeleteNode(ctx, "kb-1", onlyChild.ID)
	require.NoError(t, err)
	leaves, err = svc.GetLeaves(ctx, "kb-1", orphan.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{*orphan.TagID}, leaves.TagIDs)
	assert.Equal(t, 1, leaves.Count)
}

func TestTagTreeServiceAccessControl(t *testing.T) {
	svc, _ := newTagTreeServiceForTest(t)
	ctx := tagTreeTestCtx()

	// Another tenant without share permission is rejected on reads.
	sharingTenant := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(2))
	_, err := svc.GetTree(sharingTenant, "kb-1")
	requireAppErrorCode(t, err, werrors.ErrForbidden)

	// Reads still work for the owning tenant.
	_, err = svc.GetTree(ctx, "kb-1")
	require.NoError(t, err)
}
