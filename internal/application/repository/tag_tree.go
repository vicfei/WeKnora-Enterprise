package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// tagTreeRepository is the persistence layer of the hierarchical tag tree.
type tagTreeRepository struct {
	db *gorm.DB
}

// NewTagTreeRepository creates a new tag tree repository.
func NewTagTreeRepository(db *gorm.DB) interfaces.TagTreeRepository {
	return &tagTreeRepository{db: db}
}

// subtreePathCondition matches a node itself plus every descendant via the
// materialized path; the ESCAPE clause keeps names containing % _ \ literal.
func subtreePathCondition(db *gorm.DB, tenantID uint64, kbID, path string) *gorm.DB {
	escaped := escapeLikeKeyword(path)
	return db.Where(
		"tenant_id = ? AND knowledge_base_id = ? AND (path = ? OR path LIKE ? ESCAPE ?)",
		tenantID, kbID, path, escaped+"/%", likeEscapeChar,
	)
}

// demoteTagTreeLeaf 已随标签树 2.0(v3.1.2)退役:目录可携带原生标签,
// 建子节点/移入不再需要把父级降级为纯目录。历史行为见 git 历史。

// Create inserts a tree node atomically together with its native
// knowledge_tags row. 标签树 2.0(v3.1.2):目录可以携带原生标签——在已有
// 标签的节点下建子节点不再触发"降级硬删",父节点保留标签与全部打标关系。
func (r *tagTreeRepository) Create(
	ctx context.Context,
	tenantID uint64,
	node *types.TagTree,
	nativeTag *types.KnowledgeTag,
) (*types.TagTreeCascadeResult, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if nativeTag != nil {
			if err := tx.Create(nativeTag).Error; err != nil {
				return err
			}
		}
		return tx.Create(node).Error
	})
	if err != nil {
		return nil, err
	}
	return nil, nil
}

// AttachTag gives a tag-less node its native tag atomically: creates the
// knowledge_tags row when nativeTag is non-nil (reuse passes nil) and stamps
// the node's tag_id in one transaction. 标签树 2.0(v3.1.2).
func (r *tagTreeRepository) AttachTag(
	ctx context.Context,
	tenantID uint64,
	node *types.TagTree,
	nativeTag *types.KnowledgeTag,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if nativeTag != nil {
			if err := tx.Create(nativeTag).Error; err != nil {
				return err
			}
		}
		return tx.Model(&types.TagTree{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND id = ?", tenantID, node.KnowledgeBaseID, node.ID).
			Updates(map[string]interface{}{"tag_id": *node.TagID, "updated_at": node.UpdatedAt}).Error
	})
}

// GetByID gets a tree node by ID within a knowledge base.
func (r *tagTreeRepository) GetByID(ctx context.Context, tenantID uint64, kbID string, id string) (*types.TagTree, error) {
	var node types.TagTree
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND id = ?", tenantID, kbID, id).
		First(&node).Error; err != nil {
		return nil, err
	}
	return &node, nil
}

// GetChildByName returns the sibling under parentID (nil = root level) with
// the given name, or gorm.ErrRecordNotFound when the name is free.
func (r *tagTreeRepository) GetChildByName(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	parentID *string,
	name string,
) (*types.TagTree, error) {
	var node types.TagTree
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND name = ?", tenantID, kbID, name)
	if parentID == nil || *parentID == "" {
		query = query.Where("parent_id IS NULL")
	} else {
		query = query.Where("parent_id = ?", *parentID)
	}
	if err := query.First(&node).Error; err != nil {
		return nil, err
	}
	return &node, nil
}

// ListByKB lists every node of a knowledge base ordered for tree assembly.
func (r *tagTreeRepository) ListByKB(ctx context.Context, tenantID uint64, kbID string) ([]*types.TagTree, error) {
	var nodes []*types.TagTree
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Order("sort_order ASC, path ASC").
		Find(&nodes).Error; err != nil {
		return nil, err
	}
	return nodes, nil
}

// ListByParent lists the direct children of parentID (nil = root level).
func (r *tagTreeRepository) ListByParent(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	parentID *string,
) ([]*types.TagTree, error) {
	var nodes []*types.TagTree
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID)
	if parentID == nil || *parentID == "" {
		query = query.Where("parent_id IS NULL")
	} else {
		query = query.Where("parent_id = ?", *parentID)
	}
	if err := query.Order("sort_order ASC, path ASC").Find(&nodes).Error; err != nil {
		return nil, err
	}
	return nodes, nil
}

// GetSubtree returns root plus all of its descendants, ordered by level/path.
func (r *tagTreeRepository) GetSubtree(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	root *types.TagTree,
) ([]*types.TagTree, error) {
	var nodes []*types.TagTree
	if err := subtreePathCondition(r.db.WithContext(ctx), tenantID, kbID, root.Path).
		Order("level ASC, path ASC").
		Find(&nodes).Error; err != nil {
		return nil, err
	}
	return nodes, nil
}

// GetLeafTagIDsByPath collects tag ids of all leaf nodes at or below path.
func (r *tagTreeRepository) GetLeafTagIDsByPath(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	path string,
) ([]string, error) {
	var tagIDs []string
	if err := subtreePathCondition(r.db.WithContext(ctx).Model(&types.TagTree{}), tenantID, kbID, path).
		Where("tag_id IS NOT NULL AND tag_id != ''").
		Order("path ASC").
		Pluck("tag_id", &tagIDs).Error; err != nil {
		return nil, err
	}
	return tagIDs, nil
}

// UpdateSubtree persists recomputed nodes in one transaction, optionally
// syncing a renamed node's native tag name. 标签树 2.0:不再有降级路径。
func (r *tagTreeRepository) UpdateSubtree(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	nodes []*types.TagTree,
	renameTagID string,
	renameTagNewName string,
) (*types.TagTreeCascadeResult, error) {
	now := time.Now()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if renameTagID != "" && renameTagNewName != "" {
			if err := tx.Model(&types.KnowledgeTag{}).
				Where("tenant_id = ? AND id = ?", tenantID, renameTagID).
				Updates(map[string]interface{}{"name": renameTagNewName, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		for _, node := range nodes {
			if err := tx.Model(&types.TagTree{}).
				Where("tenant_id = ? AND id = ?", tenantID, node.ID).
				Updates(map[string]interface{}{
					"parent_id":  node.ParentID,
					"name":       node.Name,
					"path":       node.Path,
					"level":      node.Level,
					"sort_order": node.SortOrder,
					"updated_at": now,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return nil, nil
}

// PreviewSubtreeCascade computes the exact side effects a DeleteSubtreeCascade
// for the same root would have, without writing. The document count is the
// DISTINCT knowledge entries joined through knowledge_tag_relations; the
// chunk count mirrors the chunk-level untag the cascade performs.
func (r *tagTreeRepository) PreviewSubtreeCascade(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	root *types.TagTree,
) (*types.TagTreeCascadePreview, error) {
	var subtree []*types.TagTree
	if err := subtreePathCondition(r.db.WithContext(ctx), tenantID, kbID, root.Path).
		Order("level ASC, path ASC").
		Find(&subtree).Error; err != nil {
		return nil, err
	}
	tagIDs := make([]string, 0, len(subtree))
	for _, node := range subtree {
		if node.IsLeaf() {
			tagIDs = append(tagIDs, *node.TagID)
		}
	}
	preview := &types.TagTreeCascadePreview{
		Nodes:          int64(len(subtree)),
		Tags:           int64(len(tagIDs)),
		AffectedTagIDs: tagIDs,
	}
	if len(tagIDs) > 0 {
		if err := r.db.WithContext(ctx).Model(&types.Chunk{}).
			Where("tenant_id = ? AND tag_id IN (?)", tenantID, tagIDs).
			Count(&preview.Chunks).Error; err != nil {
			return nil, err
		}
		if err := r.db.WithContext(ctx).Model(&types.KnowledgeTagRelation{}).
			Where("tag_id IN (?)", tagIDs).
			Distinct("knowledge_id").
			Count(&preview.Documents).Error; err != nil {
			return nil, err
		}
	}
	return preview, nil
}

// DeleteSubtreeCascade hard-deletes root and all descendants in one
// transaction, cascading to the native tags of every node that carries one and to every
// tagging reference (chunk-level and document-level). Tagged content is
// untagged but never deleted.
func (r *tagTreeRepository) DeleteSubtreeCascade(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	root *types.TagTree,
) (*types.TagTreeCascadeResult, error) {
	result := &types.TagTreeCascadeResult{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var subtree []*types.TagTree
		if err := subtreePathCondition(tx, tenantID, kbID, root.Path).
			Order("level ASC, path ASC").
			Find(&subtree).Error; err != nil {
			return err
		}
		nodeIDs := make([]string, 0, len(subtree))
		tagIDs := make([]string, 0, len(subtree))
		for _, node := range subtree {
			nodeIDs = append(nodeIDs, node.ID)
			if node.IsLeaf() {
				tagIDs = append(tagIDs, *node.TagID)
			}
		}
		result.AffectedTagIDs = tagIDs

		if len(tagIDs) > 0 {
			chunks := tx.Model(&types.Chunk{}).
				Where("tenant_id = ? AND tag_id IN (?)", tenantID, tagIDs).
				Update("tag_id", "")
			if chunks.Error != nil {
				return chunks.Error
			}
			result.UntaggedChunks = chunks.RowsAffected

			relations := tx.Where("tag_id IN (?)", tagIDs).Delete(&types.KnowledgeTagRelation{})
			if relations.Error != nil {
				return relations.Error
			}
			result.RemovedRelations = relations.RowsAffected

			tags := tx.Where("tenant_id = ? AND id IN (?)", tenantID, tagIDs).Delete(&types.KnowledgeTag{})
			if tags.Error != nil {
				return tags.Error
			}
			result.DeletedTags = tags.RowsAffected
		}

		deleted := tx.Where("tenant_id = ? AND knowledge_base_id = ? AND id IN (?)", tenantID, kbID, nodeIDs).
			Delete(&types.TagTree{})
		if deleted.Error != nil {
			return deleted.Error
		}
		result.DeletedNodes = deleted.RowsAffected
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
