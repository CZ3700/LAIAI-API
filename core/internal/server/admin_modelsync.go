package server

// 上游模型自动发现闭环（商用迭代指令 §3.10；设计 §4.4）：
// detect（fetch-models，只读）+ apply-models/mark-removed/ignore-models（改 channels.models/ignored）
// + 后台 leader 周期自动检测（model_sync_enabled）。全在控制面，不碰 hold/capture/ledger。
// 红线：apply 只改 channels.models（可路由），绝不自动写 model_prices（定价仍手动）。

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/relay"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// csvSet：逗号分隔字符串 → 去空去重集合。
func csvSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range strings.Split(s, ",") {
		if t := strings.TrimSpace(m); t != "" {
			out[t] = true
		}
	}
	return out
}

// setToCSV：集合 → 排序后的逗号分隔字符串（稳定，便于 diff/审计）。
func setToCSV(set map[string]bool) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// modelDiff：上游模型 vs 渠道已配 vs 已定价 vs 已忽略 的差集。
type modelDiff struct {
	Missing  []string // 上游有、渠道未配、未忽略（可上货）
	Extra    []string // 渠道配了、上游已无（可下架）
	Unpriced []string // 上游有、平台未定价
}

// computeModelDiff：纯函数差集（handler 与后台 job 共用）。missing 排除已忽略。
// 名字轴对齐（对抗 review CONFIRMED）：channels.models 存"对外名"，上游 /models 返回"上游原名"，
// 二者经 mapping（对外名→上游名）换算到"上游名轴"再比，否则映射渠道会误报 missing/extra。
//   - configured：渠道已配的对外名集合；mapping：该渠道 model_mapping。
//   - missing/unpriced：上游名轴（apply 时按原名落 channels.models，无映射即对外=上游，正确）。
//   - extra：返回"对外名"（mark-removed 作用于 channels.models 的对外名）。
func computeModelDiff(upstream []string, configured, priced, ignored map[string]bool, mapping map[string]string) modelDiff {
	// 渠道已服务的上游名集合：每个对外名经 mapping 换算（无映射→原名）。
	configuredUpstream := make(map[string]bool, len(configured))
	for outward := range configured {
		if up, ok := mapping[outward]; ok && up != "" {
			configuredUpstream[up] = true
		} else {
			configuredUpstream[outward] = true
		}
	}
	upstreamSet := map[string]bool{}
	var missing, unpriced []string
	for _, m := range upstream {
		upstreamSet[m] = true
		if !configuredUpstream[m] && !ignored[m] {
			missing = append(missing, m)
		}
		if !priced[m] {
			unpriced = append(unpriced, m)
		}
	}
	// extra：渠道配了但上游已无——按对外名的映射上游是否仍在上游清单判断，报对外名。
	var extra []string
	for outward := range configured {
		up := outward
		if mapped, ok := mapping[outward]; ok && mapped != "" {
			up = mapped
		}
		if !upstreamSet[up] {
			extra = append(extra, outward)
		}
	}
	sort.Strings(missing)
	sort.Strings(unpriced)
	sort.Strings(extra)
	return modelDiff{Missing: missing, Extra: extra, Unpriced: unpriced}
}

// pricedSet：一次性读全部 model_prices 的模型名集合。
func (s *Server) pricedSet(ctx context.Context) map[string]bool {
	var prices []relay.ModelPrice
	s.db.WithContext(ctx).Find(&prices)
	out := make(map[string]bool, len(prices))
	for _, p := range prices {
		out[p.Model] = true
	}
	return out
}

// modelActionReq：apply/mark-removed/ignore 的通用请求体。
type modelActionReq struct {
	Models []string `json:"models" binding:"required"`
}

// cleanModelList：去空去重，返回集合（非法/空 → 空集合）。
func cleanModelList(models []string) map[string]bool {
	out := map[string]bool{}
	for _, m := range models {
		if t := strings.TrimSpace(m); t != "" {
			out[t] = true
		}
	}
	return out
}

// adminApplyChannelModels：POST /api/admin/channels/:id/apply-models —— 把选中的上游新模型
// 追加进 channels.models（可路由）。绝不写 model_prices（定价仍手动，验收"不自动公开未定价"）。
func (s *Server) adminApplyChannelModels(c *gin.Context) {
	s.mutateChannelModels(c, "channel_models_apply", func(cur, sel map[string]bool) map[string]bool {
		for m := range sel {
			cur[m] = true // 追加
		}
		return cur
	})
}

// adminMarkChannelModelsRemoved：POST /api/admin/channels/:id/mark-removed —— 把上游已下架的
// 模型从 channels.models 移除（手动确认；upstream 瞬时故障不自动下架）。
func (s *Server) adminMarkChannelModelsRemoved(c *gin.Context) {
	s.mutateChannelModels(c, "channel_models_remove", func(cur, sel map[string]bool) map[string]bool {
		for m := range sel {
			delete(cur, m)
		}
		return cur
	})
}

// mutateChannelModels：apply/mark-removed 共用——事务内改 channels.models + 清对应 detected + 审计 + 失效缓存。
func (s *Server) mutateChannelModels(c *gin.Context, action string, mutate func(cur, sel map[string]bool) map[string]bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	var req modelActionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "models required")
		return
	}
	sel := cleanModelList(req.Models)
	if len(sel) == 0 {
		fail(c, http.StatusBadRequest, "no valid models")
		return
	}
	var newModelsCSV string
	notFound := false
	if e := s.db.Transaction(func(tx *gorm.DB) error {
		var ch relay.Channel
		// FOR UPDATE 行锁（对抗 review CONFIRMED）：channels.models 是读-改-写，两个 admin 并发
		// apply/mark-removed 同渠道时，非锁定快照读会丢更新（第二个提交覆盖第一个）。锁定读串行化。
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "models", "detected_models").First(&ch, id).Error; err != nil {
			notFound = true
			return nil
		}
		cur := mutate(csvSet(ch.Models), sel)
		newModelsCSV = setToCSV(cur)
		// 应用/移除后，把这些模型从 detected_models 徽标里剔除（已处理）
		det := csvSet(ch.DetectedModels)
		for m := range sel {
			delete(det, m)
		}
		return tx.Model(&relay.Channel{}).Where("id = ?", id).
			Updates(map[string]any{"models": newModelsCSV, "detected_models": setToCSV(det)}).Error
	}); e != nil {
		fail(c, http.StatusInternalServerError, "update failed")
		return
	}
	if notFound {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	s.writeAudit(c, action, strconv.FormatUint(id, 10), action+": ["+strings.Join(sortedKeys(sel), " ")+"]")
	s.invalidateCache(relay.KindChannel, "") // 刷新 8s 快照：路由即时跟随 models 变化
	c.JSON(http.StatusOK, gin.H{"id": id, "models": newModelsCSV})
}

// adminIgnoreChannelModels：POST /api/admin/channels/:id/ignore-models —— 把模型加入忽略清单
// （detect 不再反复提示；上游有但运营不打算上货）。也从 detected_models 徽标剔除。
func (s *Server) adminIgnoreChannelModels(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	var req modelActionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "models required")
		return
	}
	sel := cleanModelList(req.Models)
	if len(sel) == 0 {
		fail(c, http.StatusBadRequest, "no valid models")
		return
	}
	notFound := false
	var newIgnored string
	if e := s.db.Transaction(func(tx *gorm.DB) error {
		var ch relay.Channel
		// FOR UPDATE 行锁：ignored_models/detected_models 同为读-改-写，并发 ignore 需串行化。
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "ignored_models", "detected_models").First(&ch, id).Error; err != nil {
			notFound = true
			return nil
		}
		ign := csvSet(ch.IgnoredModels)
		det := csvSet(ch.DetectedModels)
		for m := range sel {
			ign[m] = true
			delete(det, m)
		}
		newIgnored = setToCSV(ign)
		return tx.Model(&relay.Channel{}).Where("id = ?", id).
			Updates(map[string]any{"ignored_models": newIgnored, "detected_models": setToCSV(det)}).Error
	}); e != nil {
		fail(c, http.StatusInternalServerError, "update failed")
		return
	}
	if notFound {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	s.writeAudit(c, "channel_models_ignore", strconv.FormatUint(id, 10), "ignored: ["+strings.Join(sortedKeys(sel), " ")+"]")
	c.JSON(http.StatusOK, gin.H{"id": id, "ignored_models": newIgnored})
}

// b2i8：bool → int8（0/1），供 tinyint 列写入。
func b2i8(b bool) int8 {
	if b {
		return 1
	}
	return 0
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SyncChannelModels：后台 leader 周期自动检测（opt-in per channel）。对 model_sync_enabled=1 的
// 渠道 probe 上游 /models，算出新模型（上游 − 已配 − 已忽略）存 detected_models + last_sync_at。
// 只写元数据，绝不改 channels.models / model_prices（上货仍需 admin 手动 apply）。
func (s *Server) SyncChannelModels(ctx context.Context, limit int) (checked, withNew int) {
	if s.relay == nil {
		return 0, 0
	}
	var channels []relay.Channel
	if err := s.db.WithContext(ctx).Where("model_sync_enabled = 1 AND status = 1").
		Order("id").Limit(limit).Find(&channels).Error; err != nil {
		return 0, 0
	}
	priced := s.pricedSet(ctx)
	now := s.now()
	for i := range channels {
		ch := &channels[i]
		if ctx.Err() != nil {
			break
		}
		// 强制守卫拨号（对抗 review CONFIRMED 红线5）：无人值守 job 不得被误配内网 base_url 牵着打内网。
		res := s.relay.ProbeExistingChannelGuarded(ctx, ch)
		checked++
		if !res.OK {
			// 探测失败（上游抖动/不支持 /models）：只更新 last_sync_at，不动 detected（不误清徽标）
			s.db.WithContext(ctx).Model(&relay.Channel{}).Where("id = ?", ch.ID).Update("last_sync_at", now)
			continue
		}
		// detected 写入放事务内 + FOR UPDATE 重读 models/ignored（对抗 review CONFIRMED）：
		// probe 网络窗口内若有 admin 并发 apply/ignore，用循环起始的陈旧快照会把已处理项复活进徽标。
		// 事务里读最新 models/ignored/mapping 再算，与 mutateChannelModels/ignore 的行锁串行化。
		hasNew := false
		// withNew 只在事务成功提交后计（回滚则 detected 未落库，不计入日志计数）。
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var fresh relay.Channel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Select("id", "models", "ignored_models", "model_mapping").First(&fresh, ch.ID).Error; err != nil {
				return nil // 渠道并发删除：跳过
			}
			diff := computeModelDiff(res.Models, csvSet(fresh.Models), priced, csvSet(fresh.IgnoredModels), fresh.MappingPairs())
			detected := setToCSV(cleanModelList(diff.Missing))
			hasNew = detected != ""
			return tx.Model(&relay.Channel{}).Where("id = ?", ch.ID).
				Updates(map[string]any{"detected_models": detected, "last_sync_at": now}).Error
		}); err == nil && hasNew {
			withNew++
		}
	}
	return checked, withNew
}
