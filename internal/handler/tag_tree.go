package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

// TagTreeHandler handles hierarchical tag tree operations.
//
// All KB-access checks (own / org-shared / via shared agent) are performed by
// the route-level g.KBAccessRead / g.KBAccessWrite guards, which rewrite
// c.Request.Context() to carry the effective tenant ID; handlers below just
// use c.Request.Context().
type TagTreeHandler struct {
	tagTreeService interfaces.TagTreeService
}

// NewTagTreeHandler creates a new TagTreeHandler.
func NewTagTreeHandler(tagTreeService interfaces.TagTreeService) *TagTreeHandler {
	return &TagTreeHandler{tagTreeService: tagTreeService}
}

type createTagTreeNodeRequest struct {
	Name      string `json:"name"       binding:"required"`
	ParentID  string `json:"parent_id"` // empty = root level
	SortOrder int    `json:"sort_order"`
}

type updateTagTreeNodeRequest struct {
	Name      *string `json:"name"`
	SortOrder *int    `json:"sort_order"`
}

type moveTagTreeNodeRequest struct {
	ParentID string `json:"parent_id"` // empty = move to root level
}

// parseParentID converts an optional parent_id payload into a pointer; empty
// string means root level.
func parseParentID(raw string) *string {
	if raw == "" {
		return nil
	}
	return &raw
}

// GetTree godoc
// @Summary      获取标签树
// @Description  获取知识库的完整层级标签树（嵌套结构）
// @Tags         标签树管理
// @Produce      json
// @Param        id   path   string  true  "知识库ID"
// @Success      200  {object}  map[string]interface{}  "标签树（嵌套数组）"
// @Failure      400  {object}  errors.AppError         "请求参数错误"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/tag-tree [get]
func (h *TagTreeHandler) GetTree(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := secutils.SanitizeForLog(c.Param("id"))

	tree, err := h.tagTreeService.GetTree(ctx, kbID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    tree,
	})
}

// ListNodes godoc
// @Summary      获取标签树节点列表
// @Description  平铺返回知识库的标签树节点；parent_id 过滤直接子节点（"root" 或留空表示根级）
// @Tags         标签树管理
// @Produce      json
// @Param        id         path   string  true   "知识库ID"
// @Param        parent_id  query  string  false  "父节点ID（root=根级；缺省返回全部节点）"
// @Success      200  {object}  map[string]interface{}  "节点列表（平铺）"
// @Failure      400  {object}  errors.AppError         "请求参数错误"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/tag-tree/nodes [get]
func (h *TagTreeHandler) ListNodes(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := secutils.SanitizeForLog(c.Param("id"))

	var parentID *string
	if raw := strings.TrimSpace(c.Query("parent_id")); raw != "" {
		parentID = &raw
	}

	nodes, err := h.tagTreeService.ListNodes(ctx, kbID, parentID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    nodes,
	})
}

// GetNode godoc
// @Summary      获取标签树节点详情
// @Description  获取单个标签树节点
// @Tags         标签树管理
// @Produce      json
// @Param        id        path   string  true  "知识库ID"
// @Param        node_id   path   string  true  "节点ID"
// @Success      200  {object}  map[string]interface{}  "节点详情"
// @Failure      404  {object}  errors.AppError         "节点不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/tag-tree/nodes/{node_id} [get]
func (h *TagTreeHandler) GetNode(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := secutils.SanitizeForLog(c.Param("id"))
	nodeID := secutils.SanitizeForLog(c.Param("node_id"))

	node, err := h.tagTreeService.GetNode(ctx, kbID, nodeID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    node,
	})
}

// CreateNode godoc
// @Summary      创建标签树节点
// @Description  创建标签树节点；新节点即叶子并自动创建原生标签；首个子节点加入时父叶子自动降级（硬删其原生标签并解除文档关联）
// @Tags         标签树管理
// @Accept       json
// @Produce      json
// @Param        id       path   string  true  "知识库ID"
// @Param        request  body   object{name=string,parent_id=string,sort_order=int}  true  "节点信息"
// @Success      200      {object}  map[string]interface{}  "创建的节点"
// @Failure      400  {object}  errors.AppError  "参数错误"
// @Failure      404  {object}  errors.AppError  "父节点不存在"
// @Failure      409  {object}  errors.AppError  "同级重名"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/tag-tree/nodes [post]
func (h *TagTreeHandler) CreateNode(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := secutils.SanitizeForLog(c.Param("id"))

	var req createTagTreeNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Error(ctx, "Failed to bind create tag tree node payload", err)
		c.Error(errors.NewBadRequestError("请求参数不合法").WithDetails(err.Error()))
		return
	}

	node, err := h.tagTreeService.CreateNode(ctx, kbID,
		parseParentID(strings.TrimSpace(req.ParentID)),
		secutils.SanitizeForLog(req.Name), req.SortOrder)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"kb_id": kbID})
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    node,
	})
}

// AttachTagToNode godoc
// @Summary      节点转为标签
// @Description  给无标签节点(旧规则降级目录)补建原生标签;幂等
// @Tags         标签树管理
// @Produce      json
// @Success      200 {object} TagTreeNode
func (h *TagTreeHandler) AttachTagToNode(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := secutils.SanitizeForLog(c.Param("id"))
	nodeID := strings.TrimSpace(c.Param("node_id"))
	node, err := h.tagTreeService.AttachTagToNode(ctx, kbID, nodeID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"kb_id": kbID})
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": node})
}

// UpdateNode godoc
// @Summary      更新标签树节点
// @Description  更新节点名称和/或排序；重命名会重算子树路径并同步叶子节点的原生标签名
// @Tags         标签树管理
// @Accept       json
// @Produce      json
// @Param        id        path   string  true  "知识库ID"
// @Param        node_id   path   string  true  "节点ID"
// @Param        request   body   object{name=string,sort_order=int}  true  "更新信息"
// @Success      200  {object}  map[string]interface{}  "更新后的节点"
// @Failure      400  {object}  errors.AppError  "参数错误"
// @Failure      404  {object}  errors.AppError  "节点不存在"
// @Failure      409  {object}  errors.AppError  "重名冲突"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/tag-tree/nodes/{node_id} [put]
func (h *TagTreeHandler) UpdateNode(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := secutils.SanitizeForLog(c.Param("id"))
	nodeID := secutils.SanitizeForLog(c.Param("node_id"))

	var req updateTagTreeNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Error(ctx, "Failed to bind update tag tree node payload", err)
		c.Error(errors.NewBadRequestError("请求参数不合法").WithDetails(err.Error()))
		return
	}

	var name *string
	if req.Name != nil {
		sanitized := secutils.SanitizeForLog(*req.Name)
		name = &sanitized
	}

	node, err := h.tagTreeService.UpdateNode(ctx, kbID, nodeID, name, req.SortOrder)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"kb_id": kbID, "node_id": nodeID})
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    node,
	})
}

// DeleteNode godoc
// @Summary      删除标签树节点
// @Description  物理硬删除节点及其整棵子树，并级联清理叶子原生标签（knowledge_tags）、文档打标关系（knowledge_tag_relations）与 FAQ 分块标签（chunks.tag_id）；文档本身保留仅解除分类；不可恢复
// @Tags         标签树管理
// @Produce      json
// @Param        id        path   string  true  "知识库ID"
// @Param        node_id   path   string  true  "节点ID"
// @Success      200  {object}  map[string]interface{}  "级联删除统计"
// @Failure      404  {object}  errors.AppError  "节点不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/tag-tree/nodes/{node_id} [delete]
func (h *TagTreeHandler) DeleteNode(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := secutils.SanitizeForLog(c.Param("id"))
	nodeID := secutils.SanitizeForLog(c.Param("node_id"))

	result, err := h.tagTreeService.DeleteNode(ctx, kbID, nodeID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"kb_id": kbID, "node_id": nodeID})
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    result,
	})
}

// MoveNode godoc
// @Summary      移动标签树节点
// @Description  将节点移动到新父级（parent_id 留空表示移到根级）；拒绝环与同级重名；目标父级为叶子时自动降级
// @Tags         标签树管理
// @Accept       json
// @Produce      json
// @Param        id        path   string  true  "知识库ID"
// @Param        node_id   path   string  true  "节点ID"
// @Param        request   body   object{parent_id=string}  true  "目标父节点（空=根级）"
// @Success      200  {object}  map[string]interface{}  "移动后的节点"
// @Failure      400  {object}  errors.AppError  "参数错误"
// @Failure      404  {object}  errors.AppError  "目标不存在"
// @Failure      409  {object}  errors.AppError  "重名冲突"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/tag-tree/nodes/{node_id}/move [post]
func (h *TagTreeHandler) MoveNode(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := secutils.SanitizeForLog(c.Param("id"))
	nodeID := secutils.SanitizeForLog(c.Param("node_id"))

	var req moveTagTreeNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Error(ctx, "Failed to bind move tag tree node payload", err)
		c.Error(errors.NewBadRequestError("请求参数不合法").WithDetails(err.Error()))
		return
	}

	node, err := h.tagTreeService.MoveNode(ctx, kbID, nodeID, strings.TrimSpace(req.ParentID))
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"kb_id": kbID, "node_id": nodeID})
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    node,
	})
}

// GetLeaves godoc
// @Summary      获取节点下的叶子标签ID列表
// @Description  检索预过滤专用：叶子节点返回自身 tag_id，父节点按物化路径收集所有子孙叶子 tag_id；结果可直接传入 hybrid-search 的 tag_ids。注意：空列表表示该节点下无叶子标签，检索应返回空结果而非取消过滤
// @Tags         标签树管理
// @Produce      json
// @Param        id        path   string  true  "知识库ID"
// @Param        node_id   path   string  true  "节点ID"
// @Success      200  {object}  map[string]interface{}  "叶子 tag_id 列表"
// @Failure      404  {object}  errors.AppError  "节点不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/tag-tree/nodes/{node_id}/leaves [get]
func (h *TagTreeHandler) GetLeaves(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := secutils.SanitizeForLog(c.Param("id"))
	nodeID := secutils.SanitizeForLog(c.Param("node_id"))

	leaves, err := h.tagTreeService.GetLeaves(ctx, kbID, nodeID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    leaves,
	})
}
