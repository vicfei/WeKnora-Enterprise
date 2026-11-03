package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTagTreeRepoForTest(t *testing.T) (interfaces.TagTreeRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.TagTree{},
		&types.KnowledgeTag{},
		&types.KnowledgeTagRelation{},
		&types.Chunk{},
	))
	return NewTagTreeRepository(db), db
}

func tagTreeFixture(id, name, path string, level int, parentID, tagID *string) *types.TagTree {
	now := time.Now()
	return &types.TagTree{
		ID:              id,
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		ParentID:        parentID,
		Name:            name,
		Path:            path,
		Level:           level,
		TagID:           tagID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func tagTreeTagFixture(id, name string) *types.KnowledgeTag {
	now := time.Now()
	return &types.KnowledgeTag{
		ID:              id,
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		Name:            name,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func strPtr(s string) *string { return &s }

func TestTagTreeCreateInsertsNodeAndNativeTag(t *testing.T) {
	repo, db := newTagTreeRepoForTest(t)
	ctx := context.Background()

	tag := tagTreeTagFixture("tag-1", "锂电")
	node := tagTreeFixture("node-1", "锂电", "/锂电", 0, nil, strPtr("tag-1"))

	demoteResult, err := repo.Create(ctx, 1, node, tag)
	require.NoError(t, err)
	assert.Nil(t, demoteResult, "no demotion expected")

	fetched, err := repo.GetByID(ctx, 1, "kb-1", "node-1")
	require.NoError(t, err)
	assert.Equal(t, "/锂电", fetched.Path)
	assert.Equal(t, 0, fetched.Level)
	assert.True(t, fetched.IsLeaf())

	var tagCount int64
	db.Model(&types.KnowledgeTag{}).Where("id = ?", "tag-1").Count(&tagCount)
	assert.EqualValues(t, 1, tagCount, "native tag must be created atomically with the node")

	// Tenant isolation: another tenant must not see the node.
	_, err = repo.GetByID(ctx, 2, "kb-1", "node-1")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestTagTreeCreateKeepsParentLeafTag(t *testing.T) {
	// 标签树 2.0(v3.1.2)回归:目录可携带原生标签,建子节点不再"降级硬删"。
	// 父节点保留标签与全部打标关系;Create 不再产生任何级联结果。
	repo, db := newTagTreeRepoForTest(t)
	ctx := context.Background()

	parentTagID := "tag-parent"
	parent := tagTreeFixture("node-parent", "新能源", "/新能源", 0, nil, strPtr(parentTagID))
	_, err := repo.Create(ctx, 1, parent, tagTreeTagFixture(parentTagID, "新能源"))
	require.NoError(t, err)
	require.NoError(t, db.Create(&types.Chunk{
		ID: "chunk-1", TenantID: 1, KnowledgeBaseID: "kb-1", KnowledgeID: "k-1",
		Content: "faq entry", ChunkIndex: 0, TagID: parentTagID,
	}).Error)
	require.NoError(t, db.Create(&types.KnowledgeTagRelation{KnowledgeID: "k-1", TagID: parentTagID}).Error)

	childTag := tagTreeTagFixture("tag-child", "锂电")
	child := tagTreeFixture("node-child", "锂电", "/新能源/锂电", 1, strPtr("node-parent"), strPtr("tag-child"))

	result, err := repo.Create(ctx, 1, child, childTag)
	require.NoError(t, err)
	assert.Nil(t, result, "标签树 2.0: 建子节点不再触发降级级联")

	var tagCount, relCount, taggedChunks int64
	db.Model(&types.KnowledgeTag{}).Where("id = ?", parentTagID).Count(&tagCount)
	db.Model(&types.KnowledgeTagRelation{}).Where("tag_id = ?", parentTagID).Count(&relCount)
	db.Model(&types.Chunk{}).Where("tag_id = ?", parentTagID).Count(&taggedChunks)
	assert.EqualValues(t, 1, tagCount, "父节点原生标签保留")
	assert.EqualValues(t, 1, relCount, "文档打标关系保留")
	assert.EqualValues(t, 1, taggedChunks, "分块标签保留")
}
func TestTagTreeGetChildByName(t *testing.T) {
	repo, _ := newTagTreeRepoForTest(t)
	ctx := context.Background()

	root := tagTreeFixture("node-root", "新能源", "/新能源", 0, nil, nil)
	child := tagTreeFixture("node-child", "锂电", "/新能源/锂电", 1, strPtr("node-root"), strPtr("tag-x"))
	_, err := repo.Create(ctx, 1, root, nil)
	require.NoError(t, err)
	_, err = repo.Create(ctx, 1, child, tagTreeTagFixture("tag-x", "锂电"))
	require.NoError(t, err)

	// Root level.
	found, err := repo.GetChildByName(ctx, 1, "kb-1", nil, "新能源")
	require.NoError(t, err)
	assert.Equal(t, "node-root", found.ID)
	_, err = repo.GetChildByName(ctx, 1, "kb-1", nil, "锂电")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Child level.
	found, err = repo.GetChildByName(ctx, 1, "kb-1", strPtr("node-root"), "锂电")
	require.NoError(t, err)
	assert.Equal(t, "node-child", found.ID)
	_, err = repo.GetChildByName(ctx, 1, "kb-1", strPtr("node-root"), "新能源")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestTagTreeListByKBAndListByParent(t *testing.T) {
	repo, _ := newTagTreeRepoForTest(t)
	ctx := context.Background()

	a := tagTreeFixture("node-a", "行业", "/行业", 0, nil, nil)
	a.SortOrder = 2
	b := tagTreeFixture("node-b", "宏观", "/宏观", 0, nil, nil)
	b.SortOrder = 1
	_, err := repo.Create(ctx, 1, a, nil)
	require.NoError(t, err)
	_, err = repo.Create(ctx, 1, b, nil)
	require.NoError(t, err)
	child := tagTreeFixture("node-a1", "半导体", "/行业/半导体", 1, strPtr("node-a"), strPtr("tag-a1"))
	child.SortOrder = 3
	_, err = repo.Create(ctx, 1, child, tagTreeTagFixture("tag-a1", "半导体"))
	require.NoError(t, err)

	all, err := repo.ListByKB(ctx, 1, "kb-1")
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, "node-b", all[0].ID, "sort_order ASC first")
	assert.Equal(t, "node-a", all[1].ID)
	assert.Equal(t, "node-a1", all[2].ID, "path ASC breaks ties")

	roots, err := repo.ListByParent(ctx, 1, "kb-1", nil)
	require.NoError(t, err)
	require.Len(t, roots, 2)
	assert.Equal(t, "node-b", roots[0].ID)

	children, err := repo.ListByParent(ctx, 1, "kb-1", strPtr("node-a"))
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, "node-a1", children[0].ID)
}

func TestTagTreeGetSubtreeEscapesLikeWildcards(t *testing.T) {
	repo, _ := newTagTreeRepoForTest(t)
	ctx := context.Background()

	// Names containing LIKE wildcards must match literally, not as patterns.
	pct := tagTreeFixture("node-pct", "涨价50%off", "/涨价50%off", 0, nil, strPtr("tag-pct"))
	_, err := repo.Create(ctx, 1, pct, tagTreeTagFixture("tag-pct", "涨价50%off"))
	require.NoError(t, err)
	pctChild := tagTreeFixture("node-pct-child", "子类", "/涨价50%off/子类", 1, strPtr("node-pct"), strPtr("tag-pct-c"))
	_, err = repo.Create(ctx, 1, pctChild, tagTreeTagFixture("tag-pct-c", "子类"))
	require.NoError(t, err)
	lookalike := tagTreeFixture("node-lookalike", "涨价500off", "/涨价500off", 0, nil, nil)
	_, err = repo.Create(ctx, 1, lookalike, nil)
	require.NoError(t, err)

	subtree, err := repo.GetSubtree(ctx, 1, "kb-1", pct)
	require.NoError(t, err)
	require.Len(t, subtree, 2, "wildcard must not broaden the prefix match")
	assert.Equal(t, "node-pct", subtree[0].ID)
	assert.Equal(t, "node-pct-child", subtree[1].ID)
}

func TestTagTreeGetLeafTagIDsByPath(t *testing.T) {
	repo, _ := newTagTreeRepoForTest(t)
	ctx := context.Background()

	root := tagTreeFixture("node-root", "行业", "/行业", 0, nil, nil)
	_, err := repo.Create(ctx, 1, root, nil)
	require.NoError(t, err)
	leafA := tagTreeFixture("node-a", "半导体", "/行业/半导体", 1, strPtr("node-root"), strPtr("tag-a"))
	_, err = repo.Create(ctx, 1, leafA, tagTreeTagFixture("tag-a", "半导体"))
	require.NoError(t, err)
	leafB := tagTreeFixture("node-b", "新能源", "/行业/新能源", 1, strPtr("node-root"), strPtr("tag-b"))
	_, err = repo.Create(ctx, 1, leafB, tagTreeTagFixture("tag-b", "新能源"))
	require.NoError(t, err)

	tagIDs, err := repo.GetLeafTagIDsByPath(ctx, 1, "kb-1", "/行业")
	require.NoError(t, err)
	assert.Equal(t, []string{"tag-a", "tag-b"}, tagIDs)

	// An exact leaf path resolves to its own tag.
	tagIDs, err = repo.GetLeafTagIDsByPath(ctx, 1, "kb-1", "/行业/新能源")
	require.NoError(t, err)
	assert.Equal(t, []string{"tag-b"}, tagIDs)
}

func TestTagTreeUpdateSubtreeRenamesNativeTag(t *testing.T) {
	repo, db := newTagTreeRepoForTest(t)
	ctx := context.Background()

	leaf := tagTreeFixture("node-leaf", "锂电", "/新能源/锂电", 1, strPtr("node-root"), strPtr("tag-leaf"))
	_, err := repo.Create(ctx, 1, leaf, tagTreeTagFixture("tag-leaf", "锂电"))
	require.NoError(t, err)

	leaf.Name = "固态电池"
	leaf.Path = "/新能源/固态电池"
	demoteResult, err := repo.UpdateSubtree(ctx, 1, "kb-1", []*types.TagTree{leaf}, "tag-leaf", "固态电池")
	require.NoError(t, err)
	assert.Nil(t, demoteResult)

	var tag types.KnowledgeTag
	require.NoError(t, db.Where("id = ?", "tag-leaf").First(&tag).Error)
	assert.Equal(t, "固态电池", tag.Name)

	fetched, err := repo.GetByID(ctx, 1, "kb-1", "node-leaf")
	require.NoError(t, err)
	assert.Equal(t, "/新能源/固态电池", fetched.Path)
}

func TestTagTreeDeleteSubtreeCascade(t *testing.T) {
	repo, db := newTagTreeRepoForTest(t)
	ctx := context.Background()

	root := tagTreeFixture("node-root", "行业", "/行业", 0, nil, nil)
	_, err := repo.Create(ctx, 1, root, nil)
	require.NoError(t, err)
	leafA := tagTreeFixture("node-a", "半导体", "/行业/半导体", 1, strPtr("node-root"), strPtr("tag-a"))
	_, err = repo.Create(ctx, 1, leafA, tagTreeTagFixture("tag-a", "半导体"))
	require.NoError(t, err)
	leafB := tagTreeFixture("node-b", "新能源", "/行业/新能源", 1, strPtr("node-root"), strPtr("tag-b"))
	_, err = repo.Create(ctx, 1, leafB, tagTreeTagFixture("tag-b", "新能源"))
	require.NoError(t, err)
	require.NoError(t, db.Create(&types.Chunk{
		ID: "chunk-a", TenantID: 1, KnowledgeBaseID: "kb-1", KnowledgeID: "k-1",
		Content: "faq a", ChunkIndex: 0, TagID: "tag-a",
	}).Error)
	require.NoError(t, db.Create(&types.KnowledgeTagRelation{KnowledgeID: "k-1", TagID: "tag-a"}).Error)

	result, err := repo.DeleteSubtreeCascade(ctx, 1, "kb-1", root)
	require.NoError(t, err)
	assert.EqualValues(t, 3, result.DeletedNodes)
	assert.EqualValues(t, 2, result.DeletedTags)
	assert.EqualValues(t, 1, result.UntaggedChunks)
	assert.EqualValues(t, 1, result.RemovedRelations)
	assert.ElementsMatch(t, []string{"tag-a", "tag-b"}, result.AffectedTagIDs)

	// Everything tree-related is gone; the chunk survives untagged.
	var nodeCount, tagCount, relCount int64
	db.Model(&types.TagTree{}).Where("knowledge_base_id = ?", "kb-1").Count(&nodeCount)
	assert.EqualValues(t, 0, nodeCount)
	db.Model(&types.KnowledgeTag{}).Where("knowledge_base_id = ?", "kb-1").Count(&tagCount)
	assert.EqualValues(t, 0, tagCount)
	db.Model(&types.KnowledgeTagRelation{}).Count(&relCount)
	assert.EqualValues(t, 0, relCount)

	var chunk types.Chunk
	require.NoError(t, db.Where("id = ?", "chunk-a").First(&chunk).Error)
	assert.Equal(t, "", chunk.TagID)
}

func TestTagTreePreviewSubtreeCascade(t *testing.T) {
	repo, db := newTagTreeRepoForTest(t)
	ctx := context.Background()

	root := tagTreeFixture("pnode-root", "行业", "/行业", 0, nil, nil)
	_, err := repo.Create(ctx, 1, root, nil)
	require.NoError(t, err)
	leafA := tagTreeFixture("pnode-a", "半导体", "/行业/半导体", 1, strPtr("pnode-root"), strPtr("ptag-a"))
	_, err = repo.Create(ctx, 1, leafA, tagTreeTagFixture("ptag-a", "半导体"))
	require.NoError(t, err)
	leafB := tagTreeFixture("pnode-b", "新能源", "/行业/新能源", 1, strPtr("pnode-root"), strPtr("ptag-b"))
	_, err = repo.Create(ctx, 1, leafB, tagTreeTagFixture("ptag-b", "新能源"))
	require.NoError(t, err)
	require.NoError(t, db.Create(&types.Chunk{
		ID: "pchunk-a", TenantID: 1, KnowledgeBaseID: "kb-1", KnowledgeID: "pk-1",
		Content: "doc chunk", ChunkIndex: 0, TagID: "ptag-a",
	}).Error)
	require.NoError(t, db.Create(&types.Chunk{
		ID: "pchunk-b", TenantID: 1, KnowledgeBaseID: "kb-1", KnowledgeID: "pk-1",
		Content: "doc chunk 2", ChunkIndex: 1, TagID: "ptag-a",
	}).Error)
	// 同一文档两条打标关系:documents 按 DISTINCT knowledge_id 计数,应为 1。
	require.NoError(t, db.Create(&types.KnowledgeTagRelation{KnowledgeID: "pk-1", TagID: "ptag-a"}).Error)
	require.NoError(t, db.Create(&types.KnowledgeTagRelation{KnowledgeID: "pk-1", TagID: "ptag-b"}).Error)

	preview, err := repo.PreviewSubtreeCascade(ctx, 1, "kb-1", root)
	require.NoError(t, err)
	assert.EqualValues(t, 3, preview.Nodes)
	assert.EqualValues(t, 2, preview.Tags)
	assert.EqualValues(t, 1, preview.Documents)
	assert.EqualValues(t, 2, preview.Chunks)
	assert.ElementsMatch(t, []string{"ptag-a", "ptag-b"}, preview.AffectedTagIDs)

	// 预览零写入:节点/标签/关系/分块标签全部原样。
	var nodeCount, tagCount, relCount, taggedChunks int64
	db.Model(&types.TagTree{}).Where("knowledge_base_id = ?", "kb-1").Count(&nodeCount)
	db.Model(&types.KnowledgeTag{}).Where("knowledge_base_id = ?", "kb-1").Count(&tagCount)
	db.Model(&types.KnowledgeTagRelation{}).Count(&relCount)
	db.Model(&types.Chunk{}).Where("tag_id <> ''").Count(&taggedChunks)
	assert.EqualValues(t, 3, nodeCount)
	assert.EqualValues(t, 2, tagCount)
	assert.EqualValues(t, 2, relCount)
	assert.EqualValues(t, 2, taggedChunks)
}
