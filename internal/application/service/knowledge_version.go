package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// 知识文档版本管理 MVP（二开特性，台账 T11；docs/design/doc-versioning-design.md
// 的收敛版）：上传勾选"作为新版本" → 标题归一自动找旧版 → pending 待确认 →
// 确认生效/旧版自动下线。检索层零改动——版本可见性完全由既有 enable_status
// 门控承载（pending/superseded 恒 disabled）。回滚为前进无关切换：superseded 版
// 的 chunks 从未删除，重新 enable 即恢复检索，无需重解析。

// DefaultKeepVersions is how many superseded versions a group keeps when the
// KB does not override it (用户拍板：默认保留 3 版；KB 级 max_versions 可调)。
const DefaultKeepVersions = 3

var (
	// 文件扩展名：标题常源自文件名，同文档换格式不应拆组。
	versionTitleExtRe = regexp.MustCompile(`\.(pdf|docx?|xlsx?|pptx?|txt|md|csv|html?|json|epub|wps|et|dps)\z`)
	// 括号内的版本标记：第N版 / v2 / v2.1 / 修订版 / 终版 / 定稿 / 最新版…
	// 半/全角括号与方括号都认。四年数字（2024年报）不在此列——季报年报靠年份
	// 区分文档，剥了会误挂组（台账明确：只剥版本标记，不剥年份）。
	versionMarkerRe = regexp.MustCompile(
		`[（(\[【]\s*(?:第\s*[0-9一二三四五六七八九十百]+\s*版|v\.?\s*\d+(?:\.\d+)*|修订版?|终版|定稿版?|最终版|最新版|更新版)\s*[）)\]】]`)
	yearRe = regexp.MustCompile(`\d{4}`)
)

// NormalizeVersionTitle produces the version-group key: the title without
// file extension and without trailing version-marker brackets. Bracket
// content containing a 4-digit year is preserved (2024年报 ≠ 2023年报).
func NormalizeVersionTitle(title string) string {
	t := strings.TrimSpace(title)
	t = versionTitleExtRe.ReplaceAllString(strings.ToLower(t), "")
	lower := strings.ToLower(t)
	for {
		stripped := versionMarkerRe.ReplaceAllString(lower, "")
		if stripped == lower {
			break
		}
		lower = stripped
	}
	return strings.Join(strings.Fields(lower), " ")
}

// versionGroupsMatch reports whether an existing document belongs to the
// normalized group: either it already carries the group key, or it is an
// unversioned document whose normalized title is the group (legacy docs join
// their group the first time a new version is uploaded).
func versionGroupsMatch(k *types.Knowledge, group string) bool {
	if k == nil || k.ID == "" {
		return false
	}
	if k.VersionGroup != "" {
		return k.VersionGroup == group
	}
	return NormalizeVersionTitle(k.Title) == group
}

// AssignVersionGroup runs at upload time for "as new version" uploads: it
// joins the document to its title group. With existing members the upload
// lands in pending (invisible to retrieval until confirmed); a first sighting
// becomes active version 1 directly — there is nothing to confirm against.
func (s *knowledgeService) AssignVersionGroup(ctx context.Context, kbID string, knowledge *types.Knowledge) error {
	group := NormalizeVersionTitle(knowledge.Title)
	if group == "" {
		return werrors.NewBadRequestError("作为新版本上传需要可归一的标题（剥离扩展名与版本标记后为空）")
	}
	members, err := s.repo.ListKnowledgeByKnowledgeBaseID(ctx, knowledge.TenantID, kbID)
	if err != nil {
		return fmt.Errorf("version group: list members: %w", err)
	}
	maxVersion := 0
	found := false
	for _, m := range members {
		if m == nil || m.ID == knowledge.ID || !versionGroupsMatch(m, group) {
			continue
		}
		found = true
		if m.VersionGroup == "" {
			// Legacy doc joins the group at first contact: stamp it so the
			// group has a stable numbering anchor.
			m.VersionGroup = group
			m.VersionNumber = 1
			m.VersionStatus = types.VersionStatusActive
			if err := s.repo.UpdateKnowledge(ctx, m); err != nil {
				return fmt.Errorf("version group: stamp legacy member: %w", err)
			}
		}
		if m.VersionNumber > maxVersion {
			maxVersion = m.VersionNumber
		}
	}
	knowledge.VersionGroup = group
	knowledge.VersionNumber = maxVersion + 1
	if found {
		knowledge.VersionStatus = types.VersionStatusPending
		// pending stays disabled regardless of parse outcome (the finalize
		// gate keeps it out of retrieval until confirmation).
		knowledge.EnableStatus = "disabled"
		// 元数据继承：pending 新版本带上现役版的自定义元数据与标签（并集，
		// 不覆盖上传时已选的标签），用户只需编辑真正要改的部分。
		if err := s.inheritActiveVersionMetadata(ctx, kbID, members, group, knowledge); err != nil {
			return err
		}
		logger.Infof(ctx, "version group %q: new upload %s pending as v%d", group, knowledge.ID, knowledge.VersionNumber)
	} else {
		knowledge.VersionStatus = types.VersionStatusActive
		knowledge.VersionNumber = 1
		logger.Infof(ctx, "version group %q: first doc %s becomes v1 active", group, knowledge.ID)
	}
	// 组字段在 CreateKnowledge 落行之后才决定，这里必须显式回写——否则只在
	// 返回对象里可见、库里没有（E2E 第一跑的教训）。
	if err := s.repo.UpdateKnowledge(ctx, knowledge); err != nil {
		return fmt.Errorf("version group: persist membership: %w", err)
	}
	return nil
}

// inheritActiveVersionMetadata copies the group's current active version's
// custom metadata and tags onto a freshly created pending upload. Upload-time
// tag picks win; inherited tags merge in as a union. Custom metadata is only
// copied when the upload itself carries none (web uploads never do;
// datasource-synced rows may).
func (s *knowledgeService) inheritActiveVersionMetadata(
	ctx context.Context, kbID string, members []*types.Knowledge, group string, knowledge *types.Knowledge,
) error {
	var active *types.Knowledge
	for _, m := range members {
		if m == nil || m.ID == knowledge.ID || !versionGroupsMatch(m, group) {
			continue
		}
		if m.VersionStatus == types.VersionStatusActive && (active == nil || m.VersionNumber > active.VersionNumber) {
			active = m
		}
	}
	if active == nil {
		return nil
	}
	if len(knowledge.CustomMetadata) == 0 || string(knowledge.CustomMetadata) == "{}" {
		knowledge.CustomMetadata = active.CustomMetadata
	}
	activeTagIDs, err := s.versionTagIDs(ctx, active.ID)
	if err != nil {
		return fmt.Errorf("version group: read active tags: %w", err)
	}
	if len(activeTagIDs) == 0 {
		return nil
	}
	ownTagIDs, err := s.versionTagIDs(ctx, knowledge.ID)
	if err != nil {
		return fmt.Errorf("version group: read own tags: %w", err)
	}
	merged := unionTagIDs(ownTagIDs, activeTagIDs)
	if len(merged) == len(ownTagIDs) {
		return nil
	}
	if err := s.setAndAttachKnowledgeTags(ctx, knowledge.TenantID, kbID, knowledge, merged); err != nil {
		return fmt.Errorf("version group: attach inherited tags: %w", err)
	}
	return nil
}

// unionTagIDs appends IDs from inherited that own is missing, own's order
// first. Upload-time picks win; the caller skips the write when nothing new.
func unionTagIDs(own, inherited []string) []string {
	set := make(map[string]struct{}, len(own))
	for _, id := range own {
		set[id] = struct{}{}
	}
	merged := append([]string(nil), own...)
	for _, id := range inherited {
		if _, ok := set[id]; !ok {
			merged = append(merged, id)
		}
	}
	return merged
}

func (s *knowledgeService) versionTagIDs(ctx context.Context, knowledgeID string) ([]string, error) {
	tags, err := s.repo.GetKnowledgeTags(ctx, []string{knowledgeID})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(tags[knowledgeID]))
	for _, t := range tags[knowledgeID] {
		if t != nil && t.ID != "" {
			ids = append(ids, t.ID)
		}
	}
	return ids, nil
}

// setVersionChunksEnabledWithEngine flips every chunk of one version in the
// given retriever engine. 检索门控在 chunk 级（is_enabled），knowledge
// .enable_status 从不参与检索过滤——T11 首版误把 knowledge 行的 disabled
// 当成了检索门，实测（2026-10-08 检索探针）superseded/pending 版本的
// chunks 全程可被召回。所有版本流转必须走这里同步 chunk 状态。
func (s *knowledgeService) setVersionChunksEnabledWithEngine(
	ctx context.Context, engine *retriever.CompositeRetrieveEngine, knowledgeID string, enabled bool,
) error {
	chunkList, err := s.chunkService.ListChunksByKnowledgeID(ctx, knowledgeID)
	if err != nil {
		return fmt.Errorf("version group: list chunks of %s: %w", knowledgeID, err)
	}
	if len(chunkList) == 0 {
		return nil
	}
	statusMap := make(map[string]bool, len(chunkList))
	for _, c := range chunkList {
		if c != nil {
			statusMap[c.ID] = enabled
		}
	}
	if err := engine.BatchUpdateChunkEnabledStatus(ctx, statusMap); err != nil {
		return fmt.Errorf("version group: update chunk status of %s: %w", knowledgeID, err)
	}
	return nil
}

// setVersionChunksEnabled resolves the KB's retriever engine, then flips the
// version's chunks. Chunk-state failures only warn: the knowledge-row
// transition has already persisted and is the source of truth for repair.
func (s *knowledgeService) setVersionChunksEnabled(ctx context.Context, kbID, knowledgeID string, enabled bool) {
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil || kb == nil {
		logger.Warnf(ctx, "version group: load KB %s for chunk cascade failed: %v", kbID, err)
		return
	}
	engine, err := retriever.CreateRetrieveEngineForKB(ctx, s.retrieveEngine, s.ownership, kb.TenantID, kb.VectorStoreID)
	if err != nil {
		logger.Warnf(ctx, "version group: resolve retrieve engine for chunk cascade failed: %v", err)
		return
	}
	if err := s.setVersionChunksEnabledWithEngine(ctx, engine, knowledgeID, enabled); err != nil {
		logger.Warnf(ctx, "version group: chunk cascade for %s (enabled=%v) failed: %v", knowledgeID, enabled, err)
	}
}

// ConfirmKnowledgeVersion promotes a pending version: the group's previous
// active version(s) are superseded (disabled, superseded_by set) and the
// pending one goes active+enabled. Retention trims superseded versions beyond
// the KB's keep count.
func (s *knowledgeService) ConfirmKnowledgeVersion(ctx context.Context, id string) (*types.Knowledge, error) {
	k, err := s.versionMember(ctx, id, "confirm")
	if err != nil {
		return nil, err
	}
	if k.VersionStatus != types.VersionStatusPending {
		return nil, werrors.NewBadRequestError(fmt.Sprintf("版本状态为 %s，只有 pending 版本可确认", k.VersionStatus))
	}
	members, err := s.groupMembers(ctx, k)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if m.ID == k.ID || m.VersionStatus != types.VersionStatusActive {
			continue
		}
		m.VersionStatus = types.VersionStatusSuperseded
		m.SupersededBy = k.ID
		m.EnableStatus = "disabled"
		if err := s.repo.UpdateKnowledge(ctx, m); err != nil {
			return nil, fmt.Errorf("version group: supersede %s: %w", m.ID, err)
		}
		logger.Infof(ctx, "version group %q: %s (v%d) superseded by %s", k.VersionGroup, m.ID, m.VersionNumber, k.ID)
		// 检索门控在 chunk 级：必须同步关闭旧版 chunks，否则下线内容仍可被召回。
		s.setVersionChunksEnabled(ctx, m.KnowledgeBaseID, m.ID, false)
	}
	k.VersionStatus = types.VersionStatusActive
	k.SupersededBy = ""
	k.EnableStatus = "enabled"
	if err := s.repo.UpdateKnowledge(ctx, k); err != nil {
		return nil, fmt.Errorf("version group: activate %s: %w", k.ID, err)
	}
	s.setVersionChunksEnabled(ctx, k.KnowledgeBaseID, k.ID, true)
	if err := s.trimVersionGroup(ctx, k); err != nil {
		logger.Warnf(ctx, "version group %q: retention trim failed: %v", k.VersionGroup, err)
	}
	return k, nil
}

// RejectKnowledgeVersion deletes a pending upload (nothing was replaced yet,
// so the plain delete path suffices).
func (s *knowledgeService) RejectKnowledgeVersion(ctx context.Context, id string) error {
	k, err := s.versionMember(ctx, id, "reject")
	if err != nil {
		return err
	}
	if k.VersionStatus != types.VersionStatusPending {
		return werrors.NewBadRequestError(fmt.Sprintf("版本状态为 %s，只有 pending 版本可拒绝", k.VersionStatus))
	}
	return s.DeleteKnowledge(ctx, id)
}

// RollbackKnowledgeVersion re-activates a superseded version and supersedes
// the current one. 用户拍板免二次确认：这是纯管理面操作，且被替换版本的下线
// 与文档删除不同——chunks 保留、可再回滚。被回滚版的 chunks 从未删除，重新
// enable 即恢复检索，零重解析。
func (s *knowledgeService) RollbackKnowledgeVersion(ctx context.Context, id string) (*types.Knowledge, error) {
	k, err := s.versionMember(ctx, id, "rollback")
	if err != nil {
		return nil, err
	}
	if k.VersionStatus != types.VersionStatusSuperseded {
		return nil, werrors.NewBadRequestError(fmt.Sprintf("版本状态为 %s，只有已下线版本可回滚", k.VersionStatus))
	}
	members, err := s.groupMembers(ctx, k)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if m.ID == k.ID || m.VersionStatus != types.VersionStatusActive {
			continue
		}
		m.VersionStatus = types.VersionStatusSuperseded
		m.SupersededBy = k.ID
		m.EnableStatus = "disabled"
		if err := s.repo.UpdateKnowledge(ctx, m); err != nil {
			return nil, fmt.Errorf("version group: supersede %s on rollback: %w", m.ID, err)
		}
		// 回滚是双向切换：被换下的现役版 chunks 同步关闭。
		s.setVersionChunksEnabled(ctx, m.KnowledgeBaseID, m.ID, false)
	}
	k.VersionStatus = types.VersionStatusActive
	k.SupersededBy = ""
	k.EnableStatus = "enabled"
	if err := s.repo.UpdateKnowledge(ctx, k); err != nil {
		return nil, fmt.Errorf("version group: rollback activate %s: %w", k.ID, err)
	}
	s.setVersionChunksEnabled(ctx, k.KnowledgeBaseID, k.ID, true)
	logger.Infof(ctx, "version group %q: rolled back to %s (v%d)", k.VersionGroup, k.ID, k.VersionNumber)
	return k, nil
}

// ListKnowledgeVersions returns the document's version group, newest first.
func (s *knowledgeService) ListKnowledgeVersions(ctx context.Context, id string) ([]*types.Knowledge, error) {
	k, err := s.versionMember(ctx, id, "list")
	if err != nil {
		return nil, err
	}
	return s.groupMembers(ctx, k)
}

func (s *knowledgeService) versionMember(ctx context.Context, id, op string) (*types.Knowledge, error) {
	k, err := s.repo.GetKnowledgeByIDOnly(ctx, id)
	if err != nil || k == nil {
		return nil, werrors.NewBadRequestError(fmt.Sprintf("知识 %s 不存在（%s）", id, op))
	}
	if k.VersionGroup == "" {
		return nil, werrors.NewBadRequestError("该知识未加入版本组（作为新版本上传才会成组）")
	}
	return k, nil
}

func (s *knowledgeService) groupMembers(ctx context.Context, k *types.Knowledge) ([]*types.Knowledge, error) {
	members, err := s.repo.ListKnowledgeByKnowledgeBaseID(ctx, k.TenantID, k.KnowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("version group: list members: %w", err)
	}
	out := make([]*types.Knowledge, 0, 4)
	for _, m := range members {
		if m != nil && versionGroupsMatch(m, k.VersionGroup) {
			out = append(out, m)
		}
	}
	// newest first
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].VersionNumber > out[j-1].VersionNumber; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// trimVersionGroup keeps at most keep superseded versions per group (plus the
// active one) and deletes the oldest beyond the cap via the full delete path
// (chunks, vectors, storage references all cleaned).
func (s *knowledgeService) trimVersionGroup(ctx context.Context, active *types.Knowledge) error {
	keep := DefaultKeepVersions
	if s.kbService != nil {
		if kb, err := s.kbService.GetKnowledgeBaseByID(ctx, active.KnowledgeBaseID); err == nil && kb != nil && kb.MaxVersions > 0 {
			keep = kb.MaxVersions
		}
	}
	members, err := s.groupMembers(ctx, active)
	if err != nil {
		return err
	}
	superseded := make([]*types.Knowledge, 0, len(members))
	for _, m := range members {
		if m.VersionStatus == types.VersionStatusSuperseded {
			superseded = append(superseded, m)
		}
	}
	// members are newest-first; keep the newest `keep` superseded.
	for i := keep; i < len(superseded); i++ {
		old := superseded[i]
		logger.Infof(ctx, "version group %q: retention deleting superseded v%d (%s)", active.VersionGroup, old.VersionNumber, old.ID)
		if err := s.DeleteKnowledge(ctx, old.ID); err != nil {
			return fmt.Errorf("retention delete %s: %w", old.ID, err)
		}
	}
	return nil
}

// ListPendingVersions lists a KB's pending version uploads for the
// confirmation queue.
func (s *knowledgeService) ListPendingVersions(ctx context.Context, kbID string) ([]*types.VersionGroupSummary, error) {
	tenantID, _ := ctx.Value(types.TenantIDContextKey).(uint64)
	members, err := s.repo.ListKnowledgeByKnowledgeBaseID(ctx, tenantID, kbID)
	if err != nil {
		return nil, fmt.Errorf("version group: list members: %w", err)
	}
	out := []*types.VersionGroupSummary{}
	for _, m := range members {
		if m != nil && m.VersionStatus == types.VersionStatusPending {
			out = append(out, &types.VersionGroupSummary{
				KnowledgeID:   m.ID,
				Title:         m.Title,
				VersionNumber: m.VersionNumber,
				VersionStatus: m.VersionStatus,
				ParseStatus:   m.ParseStatus,
				CreatedAt:     m.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}
	}
	return out, nil
}
