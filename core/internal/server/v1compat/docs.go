// Package v1compat 提供零侵入的 /api/v1/* DTO 桥接逻辑（文档树与模型广场聚合）。
// 所有底层业务数据、定价计算与权限隔离仍由 core 既有领域包保证。
package v1compat

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
)

// APIDoc maps the api_docs table with inline translations_json.
type APIDoc struct {
	ID               uint64    `gorm:"primaryKey;column:id" json:"id"`
	ParentID         *uint64   `gorm:"column:parent_id" json:"parent_id"`
	Title            string    `gorm:"column:title" json:"title"`
	Slug             string    `gorm:"column:slug" json:"slug"`
	Content          string    `gorm:"column:content" json:"content"`
	IsDir            int8      `gorm:"column:is_dir" json:"is_dir"`
	SortOrder        int       `gorm:"column:sort_order" json:"sort_order"`
	IsActive         int8      `gorm:"column:is_active" json:"is_active"`
	CategoryID       *uint64   `gorm:"column:category_id" json:"category_id"`
	IsPreset         int8      `gorm:"column:is_preset" json:"is_preset"`
	DefaultExpanded  int8      `gorm:"column:default_expanded" json:"default_expanded"`
	TranslationsJSON string    `gorm:"column:translations_json" json:"-"`
	CreatedAt        time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (APIDoc) TableName() string { return "api_docs" }

func decodeTranslations(raw string) map[string]DocTranslationInput {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return map[string]DocTranslationInput{}
	}
	out := map[string]DocTranslationInput{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func encodeTranslations(in map[string]DocTranslationInput) string {
	clean := make(map[string]DocTranslationInput, len(in))
	for lang, tr := range in {
		l := strings.TrimSpace(lang)
		if l == "" || (strings.TrimSpace(tr.Title) == "" && strings.TrimSpace(tr.Content) == "") {
			continue
		}
		clean[l] = DocTranslationInput{
			Title:   cmp.Or(strings.TrimSpace(tr.Title), "Untitled"),
			Content: tr.Content,
		}
	}
	if len(clean) == 0 {
		return "{}"
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// DocTreeNode 是 RelayAPI.tsx / DocsManager.tsx 期望的树形节点结构。
type DocTreeNode struct {
	ID              uint64        `json:"id"`
	ParentID        *uint64       `json:"parent_id"`
	Title           string        `json:"title"`
	IsDir           bool          `json:"is_dir"`
	SortOrder       int           `json:"sort_order"`
	IsActive        bool          `json:"is_active"`
	Slug            string        `json:"slug,omitempty"`
	CategoryID      *uint64       `json:"category_id,omitempty"`
	IsPreset        bool          `json:"is_preset"`
	DefaultExpanded bool          `json:"default_expanded"`
	Children        []DocTreeNode `json:"children"`
}

// DocTranslationInput 表示单语种标题与 Markdown 正文。
type DocTranslationInput struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// DocDetailDTO 是文档详情接口返回格式。
type DocDetailDTO struct {
	ID           uint64                         `json:"id"`
	ParentID     *uint64                        `json:"parent_id"`
	Title        string                         `json:"title"`
	Content      string                         `json:"content"`
	IsDir        int8                           `json:"is_dir"`
	SortOrder    int                            `json:"sort_order"`
	IsActive     int8                           `json:"is_active"`
	CreatedAt    string                         `json:"created_at"`
	UpdatedAt    string                         `json:"updated_at"`
	Slug         string                         `json:"slug,omitempty"`
	CategoryID   *uint64                        `json:"category_id,omitempty"`
	IsPreset     int8                           `json:"is_preset"`
	Translations map[string]DocTranslationInput `json:"translations,omitempty"`
}

// ErrDocCycle 表示尝试将目录移动到自身或子孙节点下。
var ErrDocCycle = errors.New("parent cannot be self or descendant")

// ErrDocParentNotDir 表示指定的 parent_id 不是目录节点。
var ErrDocParentNotDir = errors.New("parent node must be a directory")

// IsMissingTable 识别极简测试库（未执行全部迁移）缺失扩展表的场景，以便平滑降级。
func IsMissingTable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "doesn't exist") ||
		strings.Contains(msg, "does not exist")
}

func normalizeDocLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch {
	case l == "" || l == "zh" || l == "zh-cn" || l == "zh-hans" || l == "zh-sg":
		return ""
	case strings.HasPrefix(l, "zh-tw") || strings.HasPrefix(l, "zh-hk") || strings.HasPrefix(l, "zh-mo"):
		return "zh-TW"
	case strings.HasPrefix(l, "en"):
		return "en"
	default:
		return l
	}
}

// ListDocTree 查询文档树（activeOnly=true 用于公开阅读，false 用于管理后台）。
func ListDocTree(ctx context.Context, db *gorm.DB, lang string, activeOnly bool) ([]DocTreeNode, error) {
	if err := EnsurePresetDocs(ctx, db, false); err != nil {
		if IsMissingTable(err) {
			return []DocTreeNode{}, nil
		}
		return nil, err
	}

	var docs []APIDoc
	if err := db.WithContext(ctx).Order("sort_order ASC, id ASC").Find(&docs).Error; err != nil {
		return nil, fmt.Errorf("list api_docs: %w", err)
	}

	normLang := normalizeDocLang(lang)

	roots := make([]DocTreeNode, 0, 8)
	childrenMap := make(map[uint64][]DocTreeNode)
	for _, d := range docs {
		if activeOnly && d.IsActive == 0 {
			continue
		}
		title := d.Title
		if normLang != "" {
			if tr, ok := decodeTranslations(d.TranslationsJSON)[normLang]; ok && strings.TrimSpace(tr.Title) != "" {
				title = tr.Title
			}
		}
		node := DocTreeNode{
			ID:              d.ID,
			ParentID:        d.ParentID,
			Title:           title,
			IsDir:           d.IsDir == 1,
			SortOrder:       d.SortOrder,
			IsActive:        d.IsActive == 1,
			Slug:            d.Slug,
			CategoryID:      d.CategoryID,
			IsPreset:        d.IsPreset == 1,
			DefaultExpanded: d.IsDir == 1 && d.DefaultExpanded == 1,
			Children:        []DocTreeNode{},
		}
		if d.ParentID != nil && *d.ParentID > 0 {
			childrenMap[*d.ParentID] = append(childrenMap[*d.ParentID], node)
		} else {
			roots = append(roots, node)
		}
	}

	var fillChildren func(node *DocTreeNode)
	fillChildren = func(node *DocTreeNode) {
		list := childrenMap[node.ID]
		if len(list) == 0 {
			return
		}
		slices.SortFunc(list, func(a, b DocTreeNode) int {
			if c := cmp.Compare(a.SortOrder, b.SortOrder); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})
		for i := range list {
			fillChildren(&list[i])
		}
		node.Children = list
	}

	slices.SortFunc(roots, func(a, b DocTreeNode) int {
		if c := cmp.Compare(a.SortOrder, b.SortOrder); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	for i := range roots {
		fillChildren(&roots[i])
	}
	return roots, nil
}

// GetDocDetail 获取单篇文档或目录详情（带按需语言回退与管理员全量翻译视图）。
func GetDocDetail(ctx context.Context, db *gorm.DB, id uint64, lang string, includeTranslations, activeOnly bool) (*DocDetailDTO, error) {
	if err := EnsurePresetDocs(ctx, db, false); err != nil && !IsMissingTable(err) {
		return nil, err
	}
	var doc APIDoc
	q := db.WithContext(ctx).Where("id = ?", id)
	if activeOnly {
		q = q.Where("is_active = 1")
	}
	if err := q.First(&doc).Error; err != nil {
		return nil, err
	}

	title := doc.Title
	content := doc.Content
	allTrs := decodeTranslations(doc.TranslationsJSON)
	if normLang := normalizeDocLang(lang); normLang != "" {
		if tr, ok := allTrs[normLang]; ok {
			if strings.TrimSpace(tr.Title) != "" {
				title = tr.Title
			}
			if strings.TrimSpace(tr.Content) != "" {
				content = tr.Content
			}
		}
	}

	var translations map[string]DocTranslationInput
	if includeTranslations {
		translations = allTrs
	}

	return &DocDetailDTO{
		ID:           doc.ID,
		ParentID:     doc.ParentID,
		Title:        title,
		Content:      content,
		IsDir:        doc.IsDir,
		SortOrder:    doc.SortOrder,
		IsActive:     doc.IsActive,
		CreatedAt:    doc.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    doc.UpdatedAt.UTC().Format(time.RFC3339),
		Slug:         doc.Slug,
		CategoryID:   doc.CategoryID,
		IsPreset:     doc.IsPreset,
		Translations: translations,
	}, nil
}

// DocMutationRequest 用于管理端新增或修改文档节点。
type DocMutationRequest struct {
	ParentID     *uint64                        `json:"parent_id"`
	Title        string                         `json:"title" binding:"required"`
	Content      string                         `json:"content"`
	IsDir        int8                           `json:"is_dir"`
	SortOrder    int                            `json:"sort_order"`
	IsActive     int8                           `json:"is_active"`
	Slug         string                         `json:"slug"`
	CategoryID   *uint64                        `json:"category_id"`
	Translations map[string]DocTranslationInput `json:"translations"`
}

func validateParentDir(ctx context.Context, tx *gorm.DB, selfID uint64, parentID *uint64) error {
	if parentID == nil || *parentID == 0 {
		return nil
	}
	if selfID != 0 && *parentID == selfID {
		return ErrDocCycle
	}
	var parent APIDoc
	if err := tx.WithContext(ctx).Where("id = ?", *parentID).First(&parent).Error; err != nil {
		return ErrDocParentNotDir
	}
	if parent.IsDir != 1 {
		return ErrDocParentNotDir
	}
	if selfID == 0 {
		return nil
	}
	// Walk ancestors to prevent placing a directory under its own descendant.
	visited := map[uint64]bool{selfID: true}
	cur := parent.ParentID
	for cur != nil && *cur > 0 {
		if visited[*cur] {
			return ErrDocCycle
		}
		visited[*cur] = true
		var anc APIDoc
		if err := tx.WithContext(ctx).Select("id, parent_id").Where("id = ?", *cur).First(&anc).Error; err != nil {
			break
		}
		cur = anc.ParentID
	}
	return nil
}

// CreateDoc 创建新的文档或目录，并原子保存其多语言译文。
func CreateDoc(ctx context.Context, db *gorm.DB, req DocMutationRequest) (*APIDoc, error) {
	if err := EnsurePresetDocs(ctx, db, false); err != nil && !IsMissingTable(err) {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return nil, errors.New("title is required")
	}
	slug := strings.TrimSpace(req.Slug)
	var parentID *uint64
	if req.ParentID != nil && *req.ParentID > 0 {
		v := *req.ParentID
		parentID = &v
	}

	var doc APIDoc
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateParentDir(ctx, tx, 0, parentID); err != nil {
			return err
		}
		isDir := int8(0)
		if req.IsDir == 1 {
			isDir = 1
		}
		isActive := int8(1)
		if req.IsActive == 0 {
			isActive = 0
		}
		doc = APIDoc{
			ParentID:         parentID,
			Title:            title,
			Slug:             slug,
			Content:          req.Content,
			IsDir:            isDir,
			SortOrder:        req.SortOrder,
			IsActive:         isActive,
			CategoryID:       req.CategoryID,
			IsPreset:         0,
			TranslationsJSON: encodeTranslations(req.Translations),
		}
		return tx.Create(&doc).Error
	})
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

// UpdateDoc 更新既有文档节点，校验父节点非自身或子孙目录。
func UpdateDoc(ctx context.Context, db *gorm.DB, id uint64, req DocMutationRequest) (*APIDoc, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return nil, errors.New("title is required")
	}
	var parentID *uint64
	if req.ParentID != nil && *req.ParentID > 0 {
		v := *req.ParentID
		parentID = &v
	}

	var updated APIDoc
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).First(&updated).Error; err != nil {
			return err
		}
		if err := validateParentDir(ctx, tx, id, parentID); err != nil {
			return err
		}
		isActive := int8(0)
		if req.IsActive != 0 {
			isActive = 1
		}
		updates := map[string]any{
			"parent_id":         parentID,
			"title":             title,
			"content":           req.Content,
			"sort_order":        req.SortOrder,
			"is_active":         isActive,
			"slug":              strings.TrimSpace(req.Slug),
			"category_id":       req.CategoryID,
			"translations_json": mergeTranslationsJSON(updated.TranslationsJSON, req.Translations),
		}
		if err := tx.Model(&APIDoc{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).First(&updated).Error
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func mergeTranslationsJSON(existingRaw string, updates map[string]DocTranslationInput) string {
	merged := decodeTranslations(existingRaw)
	for lang, tr := range updates {
		l := strings.TrimSpace(lang)
		if l == "" || (strings.TrimSpace(tr.Title) == "" && strings.TrimSpace(tr.Content) == "") {
			continue
		}
		merged[l] = tr
	}
	return encodeTranslations(merged)
}

// SetDocDefaultExpanded 设置目录节点是否在用户侧默认展开。
func SetDocDefaultExpanded(ctx context.Context, db *gorm.DB, id uint64, expanded bool) error {
	flag := int8(0)
	if expanded {
		flag = 1
	}
	res := db.WithContext(ctx).Model(&APIDoc{}).Where("id = ? AND is_dir = 1", id).Update("default_expanded", flag)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DeleteDoc 删除文档节点（若删除目录，级联清理子预设并将手写文档提级到父目录，防止意外丢失）。
func DeleteDoc(ctx context.Context, db *gorm.DB, id uint64) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var target APIDoc
		if err := tx.Where("id = ?", id).First(&target).Error; err != nil {
			return err
		}
		var all []APIDoc
		if err := tx.Select("id, parent_id, is_preset").Find(&all).Error; err != nil {
			return err
		}
		descendants := make(map[uint64]bool)
		var collect func(pid uint64)
		collect = func(pid uint64) {
			for _, item := range all {
				if item.ParentID != nil && *item.ParentID == pid && !descendants[item.ID] {
					descendants[item.ID] = true
					collect(item.ID)
				}
			}
		}
		collect(id)

		deleteIDs := []uint64{id}
		for _, item := range all {
			if !descendants[item.ID] {
				continue
			}
			if target.IsPreset == 1 && item.IsPreset == 0 {
				if err := tx.Model(&APIDoc{}).Where("id = ?", item.ID).Update("parent_id", target.ParentID).Error; err != nil {
					return err
				}
				continue
			}
			deleteIDs = append(deleteIDs, item.ID)
		}
		return tx.Where("id IN ?", deleteIDs).Delete(&APIDoc{}).Error
	})
}

// ClearAllDocs 清空全部文档与译文（管理员重置用）。
func ClearAllDocs(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Where("1 = 1").Delete(&APIDoc{}).Error
}
