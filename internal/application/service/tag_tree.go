package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Tag tree limits: the depth cap keeps materialized paths well below the
// varchar(1000) bound; names never contain the path separator.
const (
	MaxTagTreeDepth      = 10
	MaxTagTreePathLength = 1000
	TagTreePathSeparator = "/"
)

// tagTreeService implements TagTreeService.
type tagTreeService struct {
	kbService      interfaces.KnowledgeBaseService
	kbShareService interfaces.KBShareService
	repo           interfaces.TagTreeRepository
	tagRepo        interfaces.KnowledgeTagRepository
}

// NewTagTreeService creates a new tag tree service.
func NewTagTreeService(
	kbService interfaces.KnowledgeBaseService,
	kbShareService interfaces.KBShareService,
	repo interfaces.TagTreeRepository,
	tagRepo interfaces.KnowledgeTagRepository,
) (interfaces.TagTreeService, error) {
	return &tagTreeService{
		kbService:      kbService,
		kbShareService: kbShareService,
		repo:           repo,
		tagRepo:        tagRepo,
	}, nil
}

// resolveKBRead loads the knowledge base and enforces viewer access for
// tenants reaching a shared KB, returning the tenant that owns the KB.
func (s *tagTreeService) resolveKBRead(ctx context.Context, kbID string) (uint64, error) {
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil {
		return 0, err
	}
	callerTenantID := types.MustTenantIDFromContext(ctx)
	if kb.TenantID != callerTenantID {
		callerTenantRole := types.TenantRoleFromContext(ctx)
		hasPermission, err := s.kbShareService.HasTenantKBPermission(ctx, kbID, callerTenantID, callerTenantRole, types.OrgRoleViewer)
		if err != nil || !hasPermission {
			return 0, werrors.NewForbiddenError("无权访问该知识库")
		}
	}
	return kb.TenantID, nil
}

// resolveKBWrite loads the knowledge base for a mutating call. Ownership is
// enforced by the route guards (OwnedKBOrAdmin + KBAccessWrite); here we only
// verify existence and return the tenant owning the tree data.
func (s *tagTreeService) resolveKBWrite(ctx context.Context, kbID string) (uint64, error) {
	if kbID == "" {
		return 0, werrors.NewBadRequestError("知识库ID不能为空")
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil {
		return 0, err
	}
	return kb.TenantID, nil
}

// validateTagTreeNodeName trims and validates a node name. Reserved names and
// the path separator are rejected: the default "未分类" tag is owned by the
// native tag system and must never be cascaded away by tree operations.
func validateTagTreeNodeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", werrors.NewBadRequestError("节点名称不能为空")
	}
	if name == types.UntaggedTagName {
		return "", werrors.NewBadRequestError(fmt.Sprintf("节点名称不能使用保留名 %q", types.UntaggedTagName))
	}
	if strings.Contains(name, TagTreePathSeparator) {
		return "", werrors.NewBadRequestError("节点名称不能包含 /")
	}
	if len(name) > 255 {
		return "", werrors.NewBadRequestError("节点名称长度不能超过255个字符")
	}
	return name, nil
}

// normalizeParentID maps an empty parent id to nil (root level).
func normalizeParentID(parentID *string) *string {
	if parentID == nil || *parentID == "" {
		return nil
	}
	return parentID
}

func (s *tagTreeService) loadNode(ctx context.Context, tenantID uint64, kbID, nodeID string) (*types.TagTree, error) {
	node, err := s.repo.GetByID(ctx, tenantID, kbID, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, werrors.NewNotFoundError("标签树节点不存在")
		}
		return nil, err
	}
	return node, nil
}

// CreateNode creates a node. Every new node starts as a leaf: its native
// knowledge_tags row is created (or an identically named leftover is reused)
// atomically with the node. 标签树 2.0(v3.1.2):父级可以同时是带标签的目录,
// 建子节点不再降级、不再删除父级标签与打标关系(选谁打谁、目录也是标签).
func (s *tagTreeService) CreateNode(
	ctx context.Context,
	kbID string,
	parentID *string,
	name string,
	sortOrder int,
) (*types.TagTreeNode, error) {
	name, err := validateTagTreeNodeName(name)
	if err != nil {
		return nil, err
	}
	tenantID, err := s.resolveKBWrite(ctx, kbID)
	if err != nil {
		return nil, err
	}
	parentID = normalizeParentID(parentID)

	parentPath := ""
	level := 0
	var parent *types.TagTree
	if parentID != nil {
		parent, err = s.repo.GetByID(ctx, tenantID, kbID, *parentID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, werrors.NewNotFoundError("父节点不存在")
			}
			return nil, err
		}
		parentPath = parent.Path
		level = parent.Level + 1
		if level > MaxTagTreeDepth {
			return nil, werrors.NewBadRequestError(fmt.Sprintf("标签树层级不能超过%d层", MaxTagTreeDepth))
		}
	}

	path := TagTreePathSeparator + name
	if parentPath != "" {
		path = parentPath + TagTreePathSeparator + name
	}
	if len(path) > MaxTagTreePathLength {
		return nil, werrors.NewBadRequestError("节点路径长度超过限制")
	}

	if existing, err := s.repo.GetChildByName(ctx, tenantID, kbID, parentID, name); err == nil && existing != nil {
		return nil, werrors.NewConflictError("同级已存在同名节点")
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Every new node is a leaf: reuse an identically named native tag (e.g.
	// left over from a deleted node) or create one atomically with the node.
	var nativeTag *types.KnowledgeTag
	tagID := uuid.New().String()
	if existingTag, err := s.tagRepo.GetByName(ctx, tenantID, kbID, name); err == nil && existingTag != nil {
		tagID = existingTag.ID
		nativeTag = nil
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	} else {
		now := time.Now()
		nativeTag = &types.KnowledgeTag{
			ID:              tagID,
			TenantID:        tenantID,
			KnowledgeBaseID: kbID,
			Name:            name,
			SortOrder:       sortOrder,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
	}

	now := time.Now()
	createdBy, _ := types.UserIDFromContext(ctx)
	node := &types.TagTree{
		ID:              uuid.New().String(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		ParentID:        parentID,
		Name:            name,
		Path:            path,
		Level:           level,
		TagID:           &tagID,
		SortOrder:       sortOrder,
		CreatedBy:       createdBy,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if _, err := s.repo.Create(ctx, tenantID, node, nativeTag); err != nil {
		return nil, err
	}
	return node.ToTreeNode(), nil
}

// AttachTagToNode gives a tag-less node a native tag (标签树 2.0:旧规则下被
// 降级的目录没有标签,无法被打标;此操作为其补建/复用同名原生标签)。
// 已携带标签的节点直接返回当前状态(幂等)。
func (s *tagTreeService) AttachTagToNode(ctx context.Context, kbID string, nodeID string) (*types.TagTreeNode, error) {
	tenantID, err := s.resolveKBWrite(ctx, kbID)
	if err != nil {
		return nil, err
	}
	node, err := s.loadNode(ctx, tenantID, kbID, nodeID)
	if err != nil {
		return nil, err
	}
	if node.TagID != nil && *node.TagID != "" {
		return node.ToTreeNode(), nil
	}

	tagID := uuid.New().String()
	now := time.Now()
	var nativeTag *types.KnowledgeTag
	if existingTag, err := s.tagRepo.GetByName(ctx, tenantID, kbID, node.Name); err == nil && existingTag != nil {
		tagID = existingTag.ID
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	} else {
		nativeTag = &types.KnowledgeTag{
			ID:              tagID,
			TenantID:        tenantID,
			KnowledgeBaseID: kbID,
			Name:            node.Name,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
	}
	node.TagID = &tagID
	node.UpdatedAt = now
	if err := s.repo.AttachTag(ctx, tenantID, node, nativeTag); err != nil {
		return nil, err
	}
	return node.ToTreeNode(), nil
}

// GetTree returns the whole tree of a knowledge base as a nested forest.
func (s *tagTreeService) GetTree(ctx context.Context, kbID string) ([]*types.TagTreeNode, error) {
	tenantID, err := s.resolveKBRead(ctx, kbID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.repo.ListByKB(ctx, tenantID, kbID)
	if err != nil {
		return nil, err
	}
	return types.BuildTagTree(nodes), nil
}

// ListNodes returns a flat node list; parentID nil lists every node, while a
// non-nil parentID (or "root") lists the direct children of that node.
func (s *tagTreeService) ListNodes(ctx context.Context, kbID string, parentID *string) ([]*types.TagTreeNode, error) {
	tenantID, err := s.resolveKBRead(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if parentID != nil && (*parentID == "" || *parentID == "root") {
		parentID = nil
	}

	var nodes []*types.TagTree
	if parentID == nil {
		nodes, err = s.repo.ListByKB(ctx, tenantID, kbID)
	} else {
		if _, err := s.loadNode(ctx, tenantID, kbID, *parentID); err != nil {
			return nil, err
		}
		nodes, err = s.repo.ListByParent(ctx, tenantID, kbID, parentID)
	}
	if err != nil {
		return nil, err
	}
	result := make([]*types.TagTreeNode, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, node.ToTreeNode())
	}
	return result, nil
}

// GetNode returns a single tree node.
func (s *tagTreeService) GetNode(ctx context.Context, kbID string, nodeID string) (*types.TagTreeNode, error) {
	tenantID, err := s.resolveKBRead(ctx, kbID)
	if err != nil {
		return nil, err
	}
	node, err := s.loadNode(ctx, tenantID, kbID, nodeID)
	if err != nil {
		return nil, err
	}
	return node.ToTreeNode(), nil
}

// UpdateNode renames and/or re-sorts a node. Renaming recomputes the
// materialized paths of the subtree and syncs a leaf's native tag name.
func (s *tagTreeService) UpdateNode(
	ctx context.Context,
	kbID string,
	nodeID string,
	name *string,
	sortOrder *int,
) (*types.TagTreeNode, error) {
	tenantID, err := s.resolveKBWrite(ctx, kbID)
	if err != nil {
		return nil, err
	}
	node, err := s.loadNode(ctx, tenantID, kbID, nodeID)
	if err != nil {
		return nil, err
	}

	rename := ""
	if name != nil && strings.TrimSpace(*name) != "" {
		rename, err = validateTagTreeNodeName(*name)
		if err != nil {
			return nil, err
		}
		if rename == node.Name {
			rename = ""
		}
	}
	if rename == "" && sortOrder == nil {
		return node.ToTreeNode(), nil
	}

	if rename != "" {
		if existing, serr := s.repo.GetChildByName(ctx, tenantID, kbID, node.ParentID, rename); serr == nil && existing != nil && existing.ID != node.ID {
			return nil, werrors.NewConflictError("同级已存在同名节点")
		} else if serr != nil && !errors.Is(serr, gorm.ErrRecordNotFound) {
			return nil, serr
		}
		// A renamed leaf must keep its native tag name unique per KB.
		if node.IsLeaf() {
			if existingTag, terr := s.tagRepo.GetByName(ctx, tenantID, kbID, rename); terr == nil && existingTag != nil && existingTag.ID != *node.TagID {
				return nil, werrors.NewConflictError("标签名称已存在")
			} else if terr != nil && !errors.Is(terr, gorm.ErrRecordNotFound) {
				return nil, terr
			}
		}
	}

	if sortOrder != nil {
		node.SortOrder = *sortOrder
	}

	updated := []*types.TagTree{node}
	renameTagID := ""
	if rename != "" {
		oldPath := node.Path
		// Load the subtree with the old path before mutating the node.
		subtree, err := s.repo.GetSubtree(ctx, tenantID, kbID, node)
		if err != nil {
			return nil, err
		}
		parentPath := ""
		if idx := strings.LastIndex(oldPath, TagTreePathSeparator); idx >= 0 {
			parentPath = oldPath[:idx]
		}
		node.Name = rename
		node.Path = parentPath + TagTreePathSeparator + rename
		if len(node.Path) > MaxTagTreePathLength {
			return nil, werrors.NewBadRequestError("节点路径长度超过限制")
		}
		if node.IsLeaf() {
			renameTagID = *node.TagID
		}
		for _, descendant := range subtree {
			if descendant.ID == node.ID {
				continue
			}
			descendant.Path = node.Path + strings.TrimPrefix(descendant.Path, oldPath)
			updated = append(updated, descendant)
		}
	}

	if _, err := s.repo.UpdateSubtree(ctx, tenantID, kbID, updated, renameTagID, rename); err != nil {
		return nil, err
	}
	return node.ToTreeNode(), nil
}

// DeleteNode hard-deletes the node and its whole subtree in one transaction,
// cascading to native tags and every tagging reference. Tagged documents and
// FAQ entries survive; they are only untagged.
func (s *tagTreeService) DeleteNode(ctx context.Context, kbID string, nodeID string) (*types.TagTreeCascadeResult, error) {
	tenantID, err := s.resolveKBWrite(ctx, kbID)
	if err != nil {
		return nil, err
	}
	node, err := s.loadNode(ctx, tenantID, kbID, nodeID)
	if err != nil {
		return nil, err
	}

	result, err := s.repo.DeleteSubtreeCascade(ctx, tenantID, kbID, node)
	if err != nil {
		return nil, err
	}
	operator, _ := types.UserIDFromContext(ctx)
	logger.WarnWithFields(ctx, map[string]interface{}{
		"operator":          operator,
		"kb_id":             kbID,
		"path":              node.Path,
		"deleted_nodes":     result.DeletedNodes,
		"deleted_tags":      result.DeletedTags,
		"untagged_chunks":   result.UntaggedChunks,
		"removed_relations": result.RemovedRelations,
		"affected_tag_ids":  result.AffectedTagIDs,
	}, "Tag tree subtree hard-deleted together with native tags and tagging references")
	return result, nil
}

// PreviewDeleteNode reports what DeleteNode would cascade: how many documents
// would lose the tag (and how many chunks untagged) — read-only, no writes.
// The confirm dialog surfaces these counts before the irreversible cascade.
func (s *tagTreeService) PreviewDeleteNode(
	ctx context.Context, kbID string, nodeID string,
) (*types.TagTreeCascadePreview, error) {
	tenantID, err := s.resolveKBRead(ctx, kbID)
	if err != nil {
		return nil, err
	}
	node, err := s.loadNode(ctx, tenantID, kbID, nodeID)
	if err != nil {
		return nil, err
	}
	return s.repo.PreviewSubtreeCascade(ctx, tenantID, kbID, node)
}

// MoveNode reparents a node. Cycles, self-moves and sibling name conflicts
// are rejected; the subtree's
// paths and levels are recomputed in one transaction.
func (s *tagTreeService) MoveNode(ctx context.Context, kbID string, nodeID string, newParentID string) (*types.TagTreeNode, error) {
	newParentID = strings.TrimSpace(newParentID)
	tenantID, err := s.resolveKBWrite(ctx, kbID)
	if err != nil {
		return nil, err
	}
	node, err := s.loadNode(ctx, tenantID, kbID, nodeID)
	if err != nil {
		return nil, err
	}
	if newParentID == node.ID {
		return nil, werrors.NewBadRequestError("不能将节点移动到自身之下")
	}
	currentParentID := ""
	if node.ParentID != nil {
		currentParentID = *node.ParentID
	}
	if newParentID == currentParentID {
		return node.ToTreeNode(), nil
	}

	var newParent *types.TagTree
	newParentPath := ""
	newParentLevel := -1
	if newParentID != "" {
		newParent, err = s.repo.GetByID(ctx, tenantID, kbID, newParentID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, werrors.NewNotFoundError("目标父节点不存在")
			}
			return nil, err
		}
		if newParent.Path == node.Path || strings.HasPrefix(newParent.Path, node.Path+TagTreePathSeparator) {
			return nil, werrors.NewBadRequestError("不能将节点移动到自身或其子孙节点之下")
		}
		newParentPath = newParent.Path
		newParentLevel = newParent.Level
	}

	siblingParent := normalizeParentID(&newParentID)
	if existing, serr := s.repo.GetChildByName(ctx, tenantID, kbID, siblingParent, node.Name); serr == nil && existing != nil && existing.ID != node.ID {
		return nil, werrors.NewConflictError("目标位置已存在同名节点")
	} else if serr != nil && !errors.Is(serr, gorm.ErrRecordNotFound) {
		return nil, serr
	}

	oldPath := node.Path
	newPath := TagTreePathSeparator + node.Name
	if newParentPath != "" {
		newPath = newParentPath + TagTreePathSeparator + node.Name
	}
	if len(newPath) > MaxTagTreePathLength {
		return nil, werrors.NewBadRequestError("节点路径长度超过限制")
	}
	newLevel := newParentLevel + 1

	subtree, err := s.repo.GetSubtree(ctx, tenantID, kbID, node)
	if err != nil {
		return nil, err
	}
	maxDepthBelow := 0
	for _, descendant := range subtree {
		if depth := descendant.Level - node.Level; depth > maxDepthBelow {
			maxDepthBelow = depth
		}
	}
	if newLevel+maxDepthBelow > MaxTagTreeDepth {
		return nil, werrors.NewBadRequestError(fmt.Sprintf("标签树层级不能超过%d层", MaxTagTreeDepth))
	}

	levelDelta := newLevel - node.Level
	node.ParentID = siblingParent
	node.Path = newPath
	node.Level = newLevel
	updated := make([]*types.TagTree, 0, len(subtree))
	for _, descendant := range subtree {
		if descendant.ID == node.ID {
			continue
		}
		descendant.Path = newPath + strings.TrimPrefix(descendant.Path, oldPath)
		descendant.Level += levelDelta
		updated = append(updated, descendant)
	}
	updated = append(updated, node)

	if _, err := s.repo.UpdateSubtree(ctx, tenantID, kbID, updated, "", ""); err != nil {
		return nil, err
	}
	return node.ToTreeNode(), nil
}

// GetLeaves resolves a node to the tag ids used for hybrid-search
// pre-filtering: 标签树 2.0(B 语义)——自身标签 + 子树全部标签的并集,
// 经物化路径收集。Parent node names never leak into query text. An empty
// result means the node carries no tag and has none below it; retrieval must
// return no matches rather than drop the filter.
func (s *tagTreeService) GetLeaves(ctx context.Context, kbID string, nodeID string) (*types.TagTreeLeaves, error) {
	tenantID, err := s.resolveKBRead(ctx, kbID)
	if err != nil {
		return nil, err
	}
	node, err := s.loadNode(ctx, tenantID, kbID, nodeID)
	if err != nil {
		return nil, err
	}

	leaves := &types.TagTreeLeaves{
		NodeID: node.ID,
		Path:   node.Path,
		IsLeaf: node.IsLeaf(),
		TagIDs: []string{},
	}
	// 标签树 2.0(B 语义):任何节点都解析为"自身标签 + 子树标签"的并集。
	// path 收集条件是"等于或位于 path 之下",节点自身天然包含在内;
	// 旧实现按 IsLeaf()(=有标签)短路,目录带标签后会漏掉整个子树。
	tagIDs, err := s.repo.GetLeafTagIDsByPath(ctx, tenantID, kbID, node.Path)
	if err != nil {
		return nil, err
	}
	if tagIDs != nil {
		leaves.TagIDs = tagIDs
	}
	leaves.Count = len(leaves.TagIDs)
	return leaves, nil
}
