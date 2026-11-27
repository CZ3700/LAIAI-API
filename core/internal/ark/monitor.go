package ark

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	FuseReasonWallet = "wallet"
	FuseReasonManual = "manual"
)

var cstZone = time.FixedZone("Asia/Shanghai", 8*3600)

// Account 对应表 ark_accounts（火山方舟主账号 AK/SK 凭证）。
type Account struct {
	ID            uint64    `gorm:"primaryKey;column:id" json:"id"`
	Name          string    `gorm:"column:name" json:"name"`
	VolcAccountID string    `gorm:"column:volc_account_id" json:"volc_account_id"`
	AccessKey     string    `gorm:"column:access_key" json:"access_key"`
	SecretKeyEnc  string    `gorm:"column:secret_key_enc" json:"-"`
	SecretKeyMask string    `gorm:"column:secret_key_mask" json:"secret_key_mask"`
	Region        string    `gorm:"column:region" json:"region"`
	Remark        string    `gorm:"column:remark" json:"remark"`
	CreatedAt     time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Account) TableName() string { return "ark_accounts" }

// EndpointBinding 对应表 ark_endpoint_bindings（接入点与组织/钱包绑定与熔断状态）。
type EndpointBinding struct {
	ID                 uint64          `gorm:"primaryKey;column:id" json:"id"`
	AccountID          uint64          `gorm:"column:account_id" json:"account_id"`
	EndpointID         string          `gorm:"column:endpoint_id" json:"endpoint_id"`
	OrgID              uint64          `gorm:"column:org_id" json:"org_id"`
	UserID             uint64          `gorm:"column:user_id" json:"user_id"`
	UserUID            string          `gorm:"column:user_uid" json:"user_uid"`
	APIKeyEnc          string          `gorm:"column:api_key_enc" json:"-"`
	APIKeyMask         string          `gorm:"column:api_key_mask" json:"api_key_mask"`
	UsedQuota          decimal.Decimal `gorm:"column:used_quota" json:"used_quota"`
	WalletChargedQuota decimal.Decimal `gorm:"column:wallet_charged_quota" json:"wallet_charged_quota"`
	WalletLedgerAfter  *time.Time      `gorm:"column:wallet_ledger_after" json:"wallet_ledger_after"`
	Status             int8            `gorm:"column:status" json:"status"`
	FuseReason         string          `gorm:"column:fuse_reason" json:"fuse_reason"`
	Remark             string          `gorm:"column:remark" json:"remark"`
	CreatedAt          time.Time       `gorm:"column:created_at" json:"created_at"`
	UpdatedAt          time.Time       `gorm:"column:updated_at" json:"updated_at"`
}

func (EndpointBinding) TableName() string { return "ark_endpoint_bindings" }

// VideoTask 对应表 ark_video_tasks（来自方舟 ListVideos 与分账对账的任务缓存）。
type VideoTask struct {
	ID          uint64          `gorm:"primaryKey;column:id" json:"id"`
	AccountID   uint64          `gorm:"column:account_id" json:"account_id"`
	EndpointID  string          `gorm:"column:endpoint_id" json:"endpoint_id"`
	TaskID      string          `gorm:"column:task_id" json:"task_id"`
	Model       string          `gorm:"column:model" json:"model"`
	Status      string          `gorm:"column:status" json:"status"`
	Duration    float64         `gorm:"column:duration" json:"duration"`
	Resolution  string          `gorm:"column:resolution" json:"resolution"`
	CreatedTime string          `gorm:"column:created_time" json:"created_time"`
	SplitAmount decimal.Decimal `gorm:"column:split_amount" json:"split_amount"`
	IsEstimated bool            `gorm:"column:is_estimated" json:"is_estimated"`
	TotalTokens int64           `gorm:"column:total_tokens" json:"total_tokens"`
	SyncedAt    time.Time       `gorm:"column:synced_at" json:"synced_at"`
	RawResponse string          `gorm:"column:raw_response" json:"raw_response,omitempty"`
}

func (VideoTask) TableName() string { return "ark_video_tasks" }

// EnsureSQLiteTestTables 仅在 SQLite 测试方言下创建方舟与素材相关测试表（生产严禁运行时建表）。
func EnsureSQLiteTestTables(db *gorm.DB) error {
	if db.Dialector.Name() != "sqlite" {
		return nil
	}
	ddls := []string{
		`CREATE TABLE IF NOT EXISTS ark_accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			volc_account_id TEXT NOT NULL DEFAULT '',
			access_key TEXT NOT NULL,
			secret_key_enc TEXT NOT NULL,
			secret_key_mask TEXT NOT NULL DEFAULT '',
			region TEXT NOT NULL DEFAULT 'cn-beijing',
			remark TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS ark_endpoint_bindings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id INTEGER NOT NULL,
			endpoint_id TEXT NOT NULL,
			org_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL DEFAULT 0,
			user_uid TEXT NOT NULL DEFAULT '',
			api_key_enc TEXT NOT NULL DEFAULT '',
			api_key_mask TEXT NOT NULL DEFAULT '',
			used_quota NUMERIC NOT NULL DEFAULT 0,
			wallet_charged_quota NUMERIC NOT NULL DEFAULT 0,
			wallet_ledger_after DATETIME,
			status INTEGER NOT NULL DEFAULT 1,
			fuse_reason TEXT NOT NULL DEFAULT '',
			remark TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (account_id, endpoint_id)
		)`,
		`CREATE TABLE IF NOT EXISTS ark_video_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id INTEGER NOT NULL,
			endpoint_id TEXT NOT NULL DEFAULT '',
			task_id TEXT NOT NULL UNIQUE,
			model TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			duration REAL NOT NULL DEFAULT 0,
			resolution TEXT NOT NULL DEFAULT '',
			created_time TEXT NOT NULL DEFAULT '',
			split_amount NUMERIC NOT NULL DEFAULT 0,
			is_estimated INTEGER NOT NULL DEFAULT 1,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			synced_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			raw_response TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE TABLE IF NOT EXISTS upstream_asset_bindings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			rid TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			source_type TEXT NOT NULL DEFAULT 'channel',
			channel_config_id INTEGER NOT NULL DEFAULT 0,
			asset_base_path TEXT NOT NULL DEFAULT '',
			asset_protocol TEXT NOT NULL DEFAULT 'ark_action',
			volc_ak TEXT NOT NULL DEFAULT '',
			volc_sk_enc TEXT NOT NULL DEFAULT '',
			volc_sk_mask TEXT NOT NULL DEFAULT '',
			volc_project_name TEXT NOT NULL DEFAULT 'default',
			volc_region TEXT NOT NULL DEFAULT 'cn-beijing',
			forward_rule_id INTEGER NOT NULL DEFAULT 0,
			group_id TEXT NOT NULL DEFAULT '',
			is_active INTEGER NOT NULL DEFAULT 1,
			remark TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS plugin_asset_groups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			org_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL DEFAULT 0,
			group_id TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			plugin_ns TEXT NOT NULL DEFAULT 'asset_manager',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (group_id, plugin_ns)
		)`,
		`CREATE TABLE IF NOT EXISTS plugin_assets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			org_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL DEFAULT 0,
			asset_type TEXT NOT NULL,
			source TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'approved',
			file_name TEXT NOT NULL,
			file_url TEXT NOT NULL,
			asset_id TEXT NOT NULL,
			group_id TEXT NOT NULL DEFAULT '',
			category TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '',
			meta_fingerprint TEXT NOT NULL DEFAULT '',
			plugin_ns TEXT NOT NULL DEFAULT 'asset_manager',
			storage_provider TEXT NOT NULL DEFAULT 'tos',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, ddl := range ddls {
		if err := db.Exec(ddl).Error; err != nil {
			return err
		}
	}
	return nil
}

// MaskSecret 对敏感凭证做前缀/后四位脱敏显示，不返回明文。
func MaskSecret(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 8 {
		return "****" + trimmed[max(0, len(trimmed)-2):]
	}
	return trimmed[:4] + "****" + trimmed[len(trimmed)-4:]
}

// IsSucceededStatus 判断视频任务状态是否为终态成功。
func IsSucceededStatus(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == "succeeded" || s == "success"
}

// EstimateVideoCostCNY 按 tkeapi 2026-08-08 估算规则计算人民币金额（非成功态固定 0）。
func EstimateVideoCostCNY(model, status string, totalTokens int64) decimal.Decimal {
	if !IsSucceededStatus(status) || totalTokens <= 0 {
		return decimal.Zero
	}
	m := strings.ToLower(strings.TrimSpace(model))
	ratePerMillion := decimal.NewFromFloat(15.0)
	switch {
	case strings.Contains(m, "1-0-pro-fast"):
		ratePerMillion = decimal.NewFromFloat(4.2)
	case strings.Contains(m, "1-0-pro"):
		ratePerMillion = decimal.NewFromFloat(15.0)
	case strings.Contains(m, "seedance-2-5") || strings.Contains(m, "2-5") || strings.Contains(m, "2.5"):
		ratePerMillion = decimal.NewFromFloat(70.0)
	}
	return decimal.NewFromInt(totalTokens).
		Mul(ratePerMillion).
		Div(decimal.NewFromInt(1_000_000)).
		Round(6)
}

func parseFlexibleCSTTime(raw string) (time.Time, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, false
	}
	if sec, err := strconv.ParseInt(s, 10, 64); err == nil && sec > 1_000_000_000 {
		if sec > 1_000_000_000_000 {
			return time.UnixMilli(sec).In(cstZone), true
		}
		return time.Unix(sec, 0).In(cstZone), true
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.In(cstZone), true
		}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, cstZone); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func billCountMatchesTokens(billCount float64, totalTokens int64) bool {
	if totalTokens <= 0 || billCount <= 0 {
		return false
	}
	if int64(math.Round(billCount*1000.0)) == totalTokens {
		return true
	}
	return int64(math.Round(billCount)) == totalTokens
}

// BillMatchesVideo 对齐 tkeapi 2026-08-08 双条件精准对账：Count(千token/整数) 与 UTC+8 5 分钟槽位同时命中。
func BillMatchesVideo(bill SplitBillItem, task VideoTask) bool {
	if !IsSucceededStatus(task.Status) || !billCountMatchesTokens(bill.Count, task.TotalTokens) {
		return false
	}
	taskCST, ok1 := parseFlexibleCSTTime(task.CreatedTime)
	expCST, ok2 := parseFlexibleCSTTime(bill.ExpenseTime)
	if !ok1 || !ok2 {
		return false
	}
	ceilTask := ((taskCST.Unix() + 299) / 300) * 300
	expSec := expCST.Unix()
	// 账单出账时间应在任务创建 5 分钟对齐槽位之后或同槽内（允许 ±5 分钟窗口对齐）。
	diff := expSec - ceilTask
	return diff >= -300 && diff <= 300
}

// ReconcileBillsTx 用分账账单与待估算成功任务做 1:1 精准核销；未命中任务保持 is_estimated=true，不做错误分摊。
func ReconcileBillsTx(tx *gorm.DB, accountID uint64, bills []SplitBillItem) (matched int, err error) {
	if len(bills) == 0 {
		return 0, nil
	}
	var candidates []VideoTask
	if err := tx.Where("account_id = ? AND is_estimated = ? AND LOWER(status) IN ?",
		accountID, true, []string{"succeeded", "success"}).
		Order("created_time ASC, id ASC").
		Find(&candidates).Error; err != nil {
		return 0, err
	}
	usedIdx := make([]bool, len(candidates))
	for _, bill := range bills {
		if bill.PayableAmount.IsZero() && bill.Count == 0 {
			continue
		}
		for i := range candidates {
			if usedIdx[i] {
				continue
			}
			if BillMatchesVideo(bill, candidates[i]) {
				usedIdx[i] = true
				if uerr := tx.Model(&VideoTask{}).Where("id = ?", candidates[i].ID).Updates(map[string]any{
					"split_amount": bill.PayableAmount.Round(6),
					"is_estimated": false,
					"synced_at":    time.Now().UTC(),
				}).Error; uerr != nil {
					return matched, uerr
				}
				matched++
				break
			}
		}
	}
	return matched, nil
}

// WalletDeltaPlan 描述一次绑定的纯入账决策结果。
type WalletDeltaPlan struct {
	ActionCNY decimal.Decimal // >0 扣费, <0 退款, 0 仅校正锚点/无变动
	NewAnchor decimal.Decimal
}

// PlanArkWallet 遵循 tkeapi 2026-08-08 防虚退款机制规划扣退额度：
// 1) 新 consumption: usedCNY > max(chargedAnchorCNY, netLedgerCNY) -> 增量扣费
// 2) refund: 退款严格封顶为 max(0, netLedgerCNY - usedCNY)，绝不虚增用户余额
func PlanArkWallet(usedCNY, chargedAnchorCNY, netLedgerCNY decimal.Decimal) WalletDeltaPlan {
	used := usedCNY.Round(6)
	if used.IsNegative() {
		used = decimal.Zero
	}
	anchor := chargedAnchorCNY.Round(6)
	netLedger := netLedgerCNY.Round(6)

	effectiveCharged := anchor
	if netLedger.GreaterThan(effectiveCharged) {
		effectiveCharged = netLedger
	}

	if used.GreaterThan(effectiveCharged) {
		delta := used.Sub(effectiveCharged).Round(6)
		return WalletDeltaPlan{ActionCNY: delta, NewAnchor: used}
	}

	if used.LessThan(anchor) || netLedger.GreaterThan(used) {
		maxRefundable := netLedger.Sub(used)
		if !maxRefundable.IsPositive() {
			return WalletDeltaPlan{ActionCNY: decimal.Zero, NewAnchor: used}
		}
		overCharged := effectiveCharged.Sub(used)
		refund := overCharged
		if maxRefundable.LessThan(refund) {
			refund = maxRefundable
		}
		refund = refund.Round(6)
		if refund.IsPositive() {
			return WalletDeltaPlan{ActionCNY: refund.Neg(), NewAnchor: used}
		}
		return WalletDeltaPlan{ActionCNY: decimal.Zero, NewAnchor: used}
	}

	return WalletDeltaPlan{ActionCNY: decimal.Zero, NewAnchor: anchor}
}

var cnyReasonPattern = regexp.MustCompile(`cny=(-?[0-9]+(?:\.[0-9]+)?)`)

func bindingLedgerRefID(bindingID uint64) string {
	return fmt.Sprintf("ark_bind:%d", bindingID)
}

func queryBindingNetLedgerCNY(tx *gorm.DB, binding EndpointBinding, cnyPerUSD decimal.Decimal) (decimal.Decimal, error) {
	q := tx.Model(&ledger.JournalEntry{}).
		Where("ref_type IN ? AND ref_id = ?", []string{"ark_video_consume", "ark_video_refund"}, bindingLedgerRefID(binding.ID))
	if binding.WalletLedgerAfter != nil {
		q = q.Where("created_at >= ?", *binding.WalletLedgerAfter)
	}
	var rows []ledger.JournalEntry
	if err := q.Order("id ASC").Find(&rows).Error; err != nil {
		return decimal.Zero, err
	}
	netCNY := decimal.Zero
	for _, r := range rows {
		if m := cnyReasonPattern.FindStringSubmatch(r.Reason); len(m) == 2 {
			if cnyVal, err := decimal.NewFromString(m[1]); err == nil {
				if r.RefType == "ark_video_consume" {
					netCNY = netCNY.Add(cnyVal.Abs())
				} else {
					netCNY = netCNY.Sub(cnyVal.Abs())
				}
				continue
			}
		}
		// 兜底：若未带 cny= 标签，则由 USD delta 换算（delta 为负表示消费，正表示退款）
		netCNY = netCNY.Add(r.Delta.Neg().Mul(cnyPerUSD))
	}
	return netCNY.Round(6), nil
}

// ApplyBindingWalletDelta 根据绑定最新的 used_quota 与钱包净流水执行增量扣费或封顶退款，保持账本零漂移。
func ApplyBindingWalletDelta(
	db *gorm.DB,
	bindingID uint64,
	usedCNY decimal.Decimal,
	cnyPerUSD decimal.Decimal,
	syncNonce string,
) (WalletDeltaPlan, error) {
	if !cnyPerUSD.IsPositive() {
		cnyPerUSD = decimal.NewFromFloat(7.3)
	}
	var outPlan WalletDeltaPlan
	err := db.Transaction(func(tx *gorm.DB) error {
		var b EndpointBinding
		q := tx
		if tx.Dialector.Name() != "sqlite" {
			q = tx.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := q.First(&b, bindingID).Error; err != nil {
			return err
		}
		netLedgerCNY, err := queryBindingNetLedgerCNY(tx, b, cnyPerUSD)
		if err != nil {
			return err
		}
		plan := PlanArkWallet(usedCNY, b.WalletChargedQuota, netLedgerCNY)
		outPlan = plan

		if !plan.ActionCNY.IsZero() && b.OrgID > 0 {
			acc, err := ledger.EnsureAccount(tx, b.OrgID, ledger.AccountUserCredit)
			if err != nil {
				return err
			}
			usdDelta := plan.ActionCNY.Abs().Div(cnyPerUSD).Round(8)
			if usdDelta.IsPositive() {
				if plan.ActionCNY.IsPositive() {
					_, _, err = ledger.Post(tx, ledger.PostInput{
						IdempotencyKey: fmt.Sprintf("ark_consume:%d:%s", b.ID, cmp.Or(syncNonce, strconv.FormatInt(time.Now().UnixNano(), 10))),
						AccountID:      acc.ID,
						Kind:           ledger.KindDebit,
						Delta:          usdDelta.Neg(),
						RefType:        "ark_video_consume",
						RefID:          bindingLedgerRefID(b.ID),
						Reason:         fmt.Sprintf("方舟视频消费 ep=%s cny=%s", b.EndpointID, plan.ActionCNY.StringFixed(6)),
						AllowNegative:  true,
					})
				} else {
					_, _, err = ledger.Post(tx, ledger.PostInput{
						IdempotencyKey: fmt.Sprintf("ark_refund:%d:%s", b.ID, cmp.Or(syncNonce, strconv.FormatInt(time.Now().UnixNano(), 10))),
						AccountID:      acc.ID,
						Kind:           ledger.KindCredit,
						Delta:          usdDelta,
						RefType:        "ark_video_refund",
						RefID:          bindingLedgerRefID(b.ID),
						Reason:         fmt.Sprintf("方舟对账退差 ep=%s cny=%s", b.EndpointID, plan.ActionCNY.Abs().StringFixed(6)),
					})
				}
				if err != nil {
					return err
				}
			}
		}

		return tx.Model(&EndpointBinding{}).Where("id = ?", b.ID).Updates(map[string]any{
			"used_quota":           usedCNY.Round(6),
			"wallet_charged_quota": plan.NewAnchor.Round(6),
			"updated_at":           time.Now().UTC(),
		}).Error
	})
	return outPlan, err
}

// RecomputeEndpointUsedQuota 汇总指定接入点全部成功态任务金额（含估算与实价）。
func RecomputeEndpointUsedQuota(db *gorm.DB, accountID uint64, endpointID string) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := db.Model(&VideoTask{}).
		Select("COALESCE(SUM(split_amount), 0)").
		Where("account_id = ? AND endpoint_id = ? AND LOWER(status) IN ?", accountID, endpointID, []string{"succeeded", "success"}).
		Scan(&total).Error
	return total.Round(6), err
}

// SyncSummary 汇报一次火山方舟全量监控同步的执行统计。
type SyncSummary struct {
	AccountsProcessed  int `json:"accounts_processed"`
	BindingsSynced     int `json:"bindings_synced"`
	TasksUpserted      int `json:"tasks_upserted"`
	BillsReconciled    int `json:"bills_reconciled"`
	EndpointsFused     int `json:"endpoints_fused"`
	EndpointsRecovered int `json:"endpoints_recovered"`
}

// SyncAllAccounts 拉取活跃主账号与绑定接入点下的视频任务、执行分账对账、增量钱包扣退与熔断/恢复控制。
func SyncAllAccounts(
	ctx context.Context,
	db *gorm.DB,
	keyring *crypto.Keyring,
	cnyPerUSD decimal.Decimal,
	httpClient *http.Client,
	openAIOverride string,
) (*SyncSummary, error) {
	summary := &SyncSummary{}
	var accounts []Account
	if err := db.WithContext(ctx).Order("id ASC").Find(&accounts).Error; err != nil {
		return summary, err
	}
	now := time.Now()
	nonce := strconv.FormatInt(now.UnixNano(), 10)
	local := now.In(cstZone)
	month := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, local.Location())
	billPeriods := []string{month.AddDate(0, -1, 0).Format("2006-01"), month.Format("2006-01")}

	for _, acc := range accounts {
		var bindings []EndpointBinding
		if err := db.WithContext(ctx).Where("account_id = ?", acc.ID).Order("id ASC").Find(&bindings).Error; err != nil {
			continue
		}
		if len(bindings) == 0 {
			continue
		}
		summary.AccountsProcessed++

		sk := acc.SecretKeyEnc
		if keyring != nil && strings.HasPrefix(sk, "enc:v1:") {
			dec, derr := keyring.Decrypt(sk)
			if derr != nil {
				continue
			}
			sk = dec
		}
		arkClient := NewClient(ClientConfig{
			AccessKey:       acc.AccessKey,
			SecretKey:       sk,
			Region:          acc.Region,
			BaseURLOverride: openAIOverride,
			ThrottleScope:   fmt.Sprintf("ark_monitor:%d", acc.ID),
		}, httpClient)

		// 1. 拉取绑定接入点的视频任务并写入/更新 ark_video_tasks
		allPullOK := true
		for _, b := range bindings {
			apiKey := b.APIKeyEnc
			if keyring != nil && strings.HasPrefix(apiKey, "enc:v1:") {
				dec, derr := keyring.Decrypt(apiKey)
				if derr != nil {
					allPullOK = false
					continue
				}
				apiKey = dec
			}
			if strings.TrimSpace(apiKey) == "" {
				continue
			}
			const pageSize = 50
			bindingOK := true
			seen := make(map[string]struct{})
			for page := 1; ; page++ {
				remoteTasks, err := arkClient.PullEndpointVideoTasks(ctx, openAIOverride, apiKey, b.EndpointID, page, pageSize)
				if err != nil {
					bindingOK = false
					break
				}
				fresh := 0
				for _, rt := range remoteTasks {
					if _, visited := seen[rt.TaskID]; visited || rt.TaskID == "" {
						continue
					}
					seen[rt.TaskID] = struct{}{}
					fresh++
					if err := upsertPulledVideoTask(db.WithContext(ctx), acc.ID, b.EndpointID, rt); err != nil {
						bindingOK = false
					} else {
						summary.TasksUpserted++
					}
				}
				if len(remoteTasks) < pageSize {
					break
				}
				if fresh == 0 {
					bindingOK = false
					break
				}
			}
			if bindingOK {
				summary.BindingsSynced++
			} else {
				allPullOK = false
			}
		}

		for _, period := range billPeriods {
			const limit = 100
			for offset := 0; ; {
				bills, total, err := arkClient.ListSplitBillDetail(ctx, period, limit, offset)
				if err != nil || len(bills) == 0 {
					break
				}
				var matched int
				if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var err error
					matched, err = ReconcileBillsTx(tx, acc.ID, bills)
					return err
				}); err != nil {
					break
				}
				summary.BillsReconciled += matched
				offset += len(bills)
				if total > 0 && offset >= total || total == 0 && len(bills) < limit {
					break
				}
			}
		}

		// 如果该账号有接入点拉取失败，跳过入账以免误退款（与 tkeapi 2026-07-27 一致）
		if !allPullOK {
			continue
		}

		// 3. 汇总接入点消费 + 增量钱包扣/退 + 余额熔断/恢复
		touchedOrgs := map[uint64]struct{}{}
		for _, b := range bindings {
			usedCNY, qerr := RecomputeEndpointUsedQuota(db.WithContext(ctx), acc.ID, b.EndpointID)
			if qerr != nil {
				continue
			}
			if _, werr := ApplyBindingWalletDelta(db.WithContext(ctx), b.ID, usedCNY, cnyPerUSD, nonce); werr != nil {
				continue
			}
			if b.OrgID > 0 {
				touchedOrgs[b.OrgID] = struct{}{}
			}
		}

		// 4. 钱包熔断与自动恢复（仅处理 fuse_reason = 'wallet'，manual 管理员停用绝不自动拉起）
		for orgID := range touchedOrgs {
			availUSD, verr := ledger.OrgAvailable(db.WithContext(ctx), orgID, ledger.AccountUserCredit)
			if verr != nil {
				continue
			}
			if !availUSD.IsPositive() {
				for _, b := range bindings {
					if b.OrgID != orgID || b.Status != 1 {
						continue
					}
					_ = arkClient.StopEndpoint(ctx, b.EndpointID)
					_ = db.WithContext(ctx).Model(&EndpointBinding{}).Where("id = ?", b.ID).Updates(map[string]any{
						"status":      0,
						"fuse_reason": FuseReasonWallet,
						"updated_at":  time.Now().UTC(),
					}).Error
					summary.EndpointsFused++
				}
			} else {
				for _, b := range bindings {
					if b.OrgID != orgID || b.Status != 0 || b.FuseReason != FuseReasonWallet {
						continue
					}
					if serr := arkClient.StartEndpoint(ctx, b.EndpointID); serr == nil {
						_ = db.WithContext(ctx).Model(&EndpointBinding{}).Where("id = ?", b.ID).Updates(map[string]any{
							"status":      1,
							"fuse_reason": "",
							"updated_at":  time.Now().UTC(),
						}).Error
						summary.EndpointsRecovered++
					}
				}
			}
		}
	}
	return summary, nil
}

func upsertPulledVideoTask(db *gorm.DB, accountID uint64, endpointID string, rt RemoteVideoTask) error {
	var existing VideoTask
	err := db.Where("task_id = ?", rt.TaskID).First(&existing).Error
	if err == nil {
		updates := map[string]any{
			"status":       rt.Status,
			"duration":     rt.Duration,
			"resolution":   cmp.Or(rt.Resolution, existing.Resolution),
			"created_time": cmp.Or(rt.CreatedTime, existing.CreatedTime),
			"synced_at":    time.Now().UTC(),
			"raw_response": cmp.Or(rt.RawJSON, existing.RawResponse),
		}
		if !IsSucceededStatus(rt.Status) {
			updates["split_amount"] = decimal.Zero
			updates["is_estimated"] = true
		} else if existing.IsEstimated && rt.TotalTokens != existing.TotalTokens {
			updates["total_tokens"] = rt.TotalTokens
			updates["split_amount"] = EstimateVideoCostCNY(rt.Model, rt.Status, rt.TotalTokens)
		}
		return db.Model(&VideoTask{}).Where("id = ?", existing.ID).Updates(updates).Error
	}

	est := EstimateVideoCostCNY(rt.Model, rt.Status, rt.TotalTokens)
	row := VideoTask{
		AccountID:   accountID,
		EndpointID:  endpointID,
		TaskID:      rt.TaskID,
		Model:       cmp.Or(rt.Model, endpointID),
		Status:      rt.Status,
		Duration:    rt.Duration,
		Resolution:  rt.Resolution,
		CreatedTime: rt.CreatedTime,
		SplitAmount: est,
		IsEstimated: true,
		TotalTokens: rt.TotalTokens,
		SyncedAt:    time.Now().UTC(),
		RawResponse: cmp.Or(rt.RawJSON, "{}"),
	}
	return db.Create(&row).Error
}
