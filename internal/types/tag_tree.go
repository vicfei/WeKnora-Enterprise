package types

import "time"

// TagTree is a node of the hierarchical tag tree scoped to a knowledge base.
// It combines an adjacency list (ParentID) with a materialized path for cheap
// subtree queries. Leaf-ness is derived, never stored: TagID != nil marks a
// leaf node linked to a native knowledge_tags row, while internal nodes only
// organize the directory structure.
//
// Rows are hard-deleted by design; the tag_tree.deleted_at DB column exists
// for a possible future soft-delete switch but is deliberately absent here so
// that GORM Delete() physically removes rows (same approach as KnowledgeTag).
type TagTree struct {
	ID              string    `json:"id"                gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64    `json:"tenant_id"`
	KnowledgeBaseID string    `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index"`
	ParentID        *string   `json:"parent_id"         gorm:"type:varchar(36);index"`
	Name            string    `json:"name"              gorm:"type:varchar(255);not null"`
	Path            string    `json:"path"              gorm:"type:varchar(1000);not null;index"`
	Level           int       `json:"level"             gorm:"not null;default:0"`
	TagID           *string   `json:"tag_id"            gorm:"type:varchar(36);index"`
	IsAutoExtract   bool      `json:"is_auto_extract"   gorm:"not null;default:false"`
	SortOrder       int       `json:"sort_order"        gorm:"not null;default:0"`
	CreatedBy       string    `json:"created_by"        gorm:"type:varchar(36)"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (TagTree) TableName() string { return "tag_tree" }

// IsLeaf reports whether the node can tag knowledge entries; true iff it is
// linked to a native tag.
func (t *TagTree) IsLeaf() bool { return t != nil && t.TagID != nil && *t.TagID != "" }

// TagTreeCascadeResult reports the side effects of a subtree hard-delete or a
// leaf demotion; consumed by API responses and WARN-level audit logs.
type TagTreeCascadeResult struct {
	DeletedNodes     int64    `json:"deleted_nodes"`
	DeletedTags      int64    `json:"deleted_tags"`
	UntaggedChunks   int64    `json:"untagged_chunks"`
	RemovedRelations int64    `json:"removed_relations"`
	AffectedTagIDs   []string `json:"affected_tag_ids"`
}

// TagTreeCascadePreview reports what a subtree hard-delete WOULD do, without
// performing it: how many documents would lose the tag (distinct knowledge
// entries in knowledge_tag_relations), how many chunks would be untagged,
// plus the node/tag footprint. The confirm dialog shows these counts so the
// blast radius is visible BEFORE the irreversible cascade runs.
type TagTreeCascadePreview struct {
	Nodes            int64    `json:"nodes"`
	Tags             int64    `json:"tags"`
	Documents        int64    `json:"documents"`
	Chunks           int64    `json:"chunks"`
	AffectedTagIDs   []string `json:"affected_tag_ids"`
}

// TagTreeLeaves is the retrieval pre-filter resolution of a tree node: the
// leaf tag ids to feed into hybrid-search. Internal nodes collect every
// descendant leaf; leaf nodes resolve to their own single tag id.
type TagTreeLeaves struct {
	NodeID string   `json:"node_id"`
	Path   string   `json:"path"`
	IsLeaf bool     `json:"is_leaf"`
	TagIDs []string `json:"tag_ids"`
	Count  int      `json:"count"`
}

// TagTreeNode is the nested representation of a tag tree returned by the API.
type TagTreeNode struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	ParentID        *string        `json:"parent_id"`
	Path            string         `json:"path"`
	Level           int            `json:"level"`
	TagID           *string        `json:"tag_id"`
	IsLeaf          bool           `json:"is_leaf"`
	IsAutoExtract   bool           `json:"is_auto_extract"`
	SortOrder       int            `json:"sort_order"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Children        []*TagTreeNode `json:"children"`
}

// ToTreeNode converts a flat TagTree row into its nested API representation.
func (t *TagTree) ToTreeNode() *TagTreeNode {
	if t == nil {
		return nil
	}
	return &TagTreeNode{
		ID:            t.ID,
		Name:          t.Name,
		ParentID:      t.ParentID,
		Path:          t.Path,
		Level:         t.Level,
		TagID:         t.TagID,
		IsLeaf:        t.IsLeaf(),
		IsAutoExtract: t.IsAutoExtract,
		SortOrder:     t.SortOrder,
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
		Children:      []*TagTreeNode{},
	}
}

// BuildTagTree assembles a nested forest from flat nodes of one knowledge
// base. Children keep the ordering of the input slice (callers pass nodes
// sorted by sort_order, then path).
func BuildTagTree(nodes []*TagTree) []*TagTreeNode {
	childrenByParent := make(map[string][]*TagTreeNode, len(nodes))
	roots := make([]*TagTreeNode, 0)
	for _, node := range nodes {
		treeNode := node.ToTreeNode()
		if node.ParentID == nil || *node.ParentID == "" {
			roots = append(roots, treeNode)
		} else {
			childrenByParent[*node.ParentID] = append(childrenByParent[*node.ParentID], treeNode)
		}
	}
	var attach func(nodes []*TagTreeNode)
	attach = func(nodes []*TagTreeNode) {
		for _, treeNode := range nodes {
			treeNode.Children = childrenByParent[treeNode.ID]
			attach(treeNode.Children)
		}
	}
	attach(roots)
	return roots
}
