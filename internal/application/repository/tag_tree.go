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

// demoteTagTreeLeaf turns a leaf node into an internal node on the caller's
// transaction: it clears chunk-level references, deletes document tag
// relations, hard-deletes the native tag, and NULLs the node's tag_id.
// Tagged documents and FAQ entries survive; they only lose this
// classification.
func demoteTagTreeLeaf(tx *gorm.DB, tenantID uint64, kbID, nodeID, tagID string) (*types.TagTreeCascadeResult, error) {
	result := &types.TagTreeCascadeResult{AffectedTagIDs: []string{tagID}}

	chunks := tx.Model(&types.Chunk{}).
		Where("tenant_id = ? AND tag_id = ?", tenantID, tagID).
		Update("tag_id", "")
	if chunks.Error != nil {
		return nil, chunks.Error
	}
	result.UntaggedChunks = chunks.RowsAffected

	relations := tx.Where("tag_id = ?", tagID).Delete(&types.KnowledgeTagRelation{})
	if relations.Error != nil {
		return nil, relations.Error
	}
	result.RemovedRelations = relations.RowsAffected

	tags := tx.Where("tenant_id = ? AND id = ?", tenantID, tagID).Delete(&types.KnowledgeTag{})
	if tags.Error != nil {
		return nil, tags.Error
	}
	result.DeletedTags = tags.RowsAffected

	if err := tx.Model(&types.TagTree{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND id = ?", tenantID, kbID, nodeID).
		Updates(map[string]interface{}{"tag_id": nil, "updated_at": time.Now()}).Error; err != nil {
		return nil, err
	}
	return result, nil
}

// Create inserts a tree node atomically: an optional parent-leaf demotion and
// the leaf's native knowledge_tags row share the node's transaction.
func (r *tagTreeRepository) Create(
	ctx context.Context,
	tenantID uint64,
	node *types.TagTree,
	nativeTag *types.KnowledgeTag,
	demoteNodeID string,
	demoteTagID string,
) (*types.TagTreeCascadeResult, error) {
	var demoteResult *types.TagTreeCascadeResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if demoteNodeID != "" && demoteTagID != "" {
			result, err := demoteTagTreeLeaf(tx, tenantID, node.KnowledgeBaseID, demoteNodeID, demoteTagID)
			if err != nil {
				return err
			}
			demoteResult = result
		}
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
	return demoteResult, nil
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
// syncing a renamed leaf's native tag name and demoting a target parent leaf.
func (r *tagTreeRepository) UpdateSubtree(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	nodes []*types.TagTree,
	renameTagID string,
	renameTagNewName string,
	demoteNodeID string,
	demoteTagID string,
) (*types.TagTreeCascadeResult, error) {
	var demoteResult *types.TagTreeCascadeResult
	now := time.Now()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if demoteNodeID != "" && demoteTagID != "" {
			result, err := demoteTagTreeLeaf(tx, tenantID, kbID, demoteNodeID, demoteTagID)
			if err != nil {
				return err
			}
			demoteResult = result
		}
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
	return demoteResult, nil
}

// DeleteSubtreeCascade hard-deletes root and all descendants in one
// transaction, cascading to the native tags of leaf nodes and to every
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
