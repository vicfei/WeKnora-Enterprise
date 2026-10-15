package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// TagTreeRepository defines persistence operations for the hierarchical tag
// tree. All methods are scoped by tenant and knowledge base.
type TagTreeRepository interface {
	// Create inserts a tree node atomically: when nativeTag is non-nil the
	// node's native knowledge_tags row is created in the same transaction.
	// 标签树 2.0(v3.1.2):目录可携带原生标签,建子节点不再降级父级。
	Create(
		ctx context.Context,
		tenantID uint64,
		node *types.TagTree,
		nativeTag *types.KnowledgeTag,
	) (*types.TagTreeCascadeResult, error)
	GetByID(ctx context.Context, tenantID uint64, kbID string, id string) (*types.TagTree, error)
	// GetChildByName returns the sibling under parentID (nil = root level)
	// with the given name, or gorm.ErrRecordNotFound when the name is free.
	GetChildByName(
		ctx context.Context,
		tenantID uint64,
		kbID string,
		parentID *string,
		name string,
	) (*types.TagTree, error)
	ListByKB(ctx context.Context, tenantID uint64, kbID string) ([]*types.TagTree, error)
	ListByParent(ctx context.Context, tenantID uint64, kbID string, parentID *string) ([]*types.TagTree, error)
	// GetSubtree returns root plus all of its descendants, ordered by level
	// and path.
	GetSubtree(ctx context.Context, tenantID uint64, kbID string, root *types.TagTree) ([]*types.TagTree, error)
	// GetLeafTagIDsByPath collects tag ids of all leaf nodes whose path equals
	// the given path or lives below it (retrieval pre-filtering).
	GetLeafTagIDsByPath(ctx context.Context, tenantID uint64, kbID string, path string) ([]string, error)
	// UpdateSubtree persists recomputed parent/name/path/level/sort_order for
	// a set of nodes in one transaction. A renamed leaf syncs its native
	// knowledge_tags.name; demoteNodeID/demoteTagID demote the target parent
	// leaf before nodes are written back (move-into-leaf case).
	// UpdateSubtree persists recomputed parent/name/path/level/sort_order for
	// a set of nodes in one transaction. A renamed node syncs its native
	// knowledge_tags.name. 标签树 2.0:不再有降级路径。
	UpdateSubtree(
		ctx context.Context,
		tenantID uint64,
		kbID string,
		nodes []*types.TagTree,
		renameTagID string,
		renameTagNewName string,
	) (*types.TagTreeCascadeResult, error)
	// DeleteSubtreeCascade hard-deletes root and all descendants together with
	// the native tags of every node that carries one and every tagging
	// reference, in one transaction. Documents and FAQ entries survive; they
	// are only untagged.
	DeleteSubtreeCascade(
		ctx context.Context,
		tenantID uint64,
		kbID string,
		root *types.TagTree,
	) (*types.TagTreeCascadeResult, error)
}

// TagTreeService defines operations on the hierarchical tag tree of a
// knowledge base. Internal nodes only organize the directory; leaf nodes are
// the only taggable units (tag_id IS NOT NULL <=> leaf).
type TagTreeService interface {
	// CreateNode creates a node; every new node starts as a leaf and gets a
	// native knowledge_tags row. parentID nil means root level. Adding the
	// first child under a leaf demotes it (native tag hard-deleted, tagged
	// documents only lose the classification).
	CreateNode(
		ctx context.Context,
		kbID string,
		parentID *string,
		name string,
		sortOrder int,
	) (*types.TagTreeNode, error)
	GetTree(ctx context.Context, kbID string) ([]*types.TagTreeNode, error)
	ListNodes(ctx context.Context, kbID string, parentID *string) ([]*types.TagTreeNode, error)
	GetNode(ctx context.Context, kbID string, nodeID string) (*types.TagTreeNode, error)
	// UpdateNode renames and/or re-sorts a node; renaming recomputes the
	// materialized paths of the whole subtree and syncs the native tag name of
	// leaf nodes.
	UpdateNode(
		ctx context.Context,
		kbID string,
		nodeID string,
		name *string,
		sortOrder *int,
	) (*types.TagTreeNode, error)
	// DeleteNode hard-deletes the node and its whole subtree in one
	// transaction, cascading to native tags and tagging references.
	DeleteNode(ctx context.Context, kbID string, nodeID string) (*types.TagTreeCascadeResult, error)
	// MoveNode reparents a node, rejecting cycles and self-moves; moving into
	// a leaf demotes that leaf first.
	MoveNode(ctx context.Context, kbID string, nodeID string, newParentID string) (*types.TagTreeNode, error)
	// GetLeaves resolves a node to leaf tag ids for hybrid-search
	// pre-filtering. Parent node names never leak into query text.
	GetLeaves(ctx context.Context, kbID string, nodeID string) (*types.TagTreeLeaves, error)
}
