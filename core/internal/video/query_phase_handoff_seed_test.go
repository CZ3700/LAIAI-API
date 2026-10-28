package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func (h *phaseHarness) loadSeed() error {
	path := filepath.Join(h.e.dir, "seed-private.json")
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		h.seed.Tasks = map[string]phaseSeedTask{}
		return nil
	}
	if e != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 65536 {
		return errPhaseHarness
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var seed phaseSeed
	if _, e = phaseStrictObject(raw, &seed); e != nil {
		return e
	}
	if seed.Protocol != phaseProtocol || seed.Run != h.e.run || seed.Case != h.e.caseID || (seed.Profile != "inbox" && seed.Profile != "legacy") || len(seed.PlainAPIKey) < 20 || len(seed.Tasks) > 8 {
		return errPhaseHarness
	}
	for _, v := range append([]string{seed.OrgID, seed.KeyID, seed.UserID, seed.ScopeID, seed.ChannelID}, seed.Actors[:]...) {
		if _, e = phaseID(v); e != nil {
			return e
		}
	}
	for handle, item := range seed.Tasks {
		if !phaseHandleValid(handle) || item.TaskID == "" || item.OperationID == "" || item.CreateRequestID == "" || len(item.BodySHA) != 64 || item.IdempotencyKey == "" {
			return errPhaseHarness
		}
	}
	h.seed = seed
	return nil
}
func (h *phaseHarness) saveSeed() error {
	_, e := phaseWrite(filepath.Join(h.e.dir, "seed-private.json"), h.seed, true)
	return e
}
func phaseNum(v uint64) string { return strconv.FormatUint(v, 10) }

func (h *phaseHarness) seedConfiguration(profile string) error {
	if h.seed.Protocol != "" || (strings.HasSuffix(h.e.caseID, "_legacy") != (profile == "legacy")) {
		return errPhaseHarness
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var count int64
	if e := h.db.WithContext(ctx).Model(&task.Task{}).Count(&count).Error; e != nil || count != 0 {
		return errPhaseHarness
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	seed := phaseSeed{Protocol: phaseProtocol, Run: h.e.run, Case: h.e.caseID, Profile: profile, Tasks: map[string]phaseSeedTask{}}
	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user, org, e := identity.Register(tx, identity.RegisterInput{Email: "phase-" + h.e.run + "@example.invalid", Password: "Synthetic-Phase-Password-2026!", DisplayName: "Synthetic phase owner", EmailVerified: true})
		if e != nil {
			return e
		}
		plain, key, e := identity.IssueKey(tx, identity.IssueKeyInput{OrgID: org.ID, UserID: &user.ID, Name: "phase public API", Scopes: "video:*", ModelLimits: ark.Model20})
		if e != nil {
			return e
		}
		seed.OrgID, seed.KeyID, seed.UserID, seed.PlainAPIKey = phaseNum(org.ID), phaseNum(key.ID), phaseNum(user.ID), plain
		account, e := ledger.EnsureAccount(tx, org.ID, ledger.AccountUserCredit)
		if e != nil {
			return e
		}
		if _, _, e = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "phase-credit-" + h.e.run, AccountID: account.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(100)}); e != nil {
			return e
		}
		for i := range seed.Actors {
			actor, _, e := identity.Register(tx, identity.RegisterInput{Email: fmt.Sprintf("phase-%s-admin-%d@example.invalid", h.e.run, i), Password: "Synthetic-Phase-Password-2026!", DisplayName: "Synthetic individual reviewer", EmailVerified: true})
			if e != nil {
				return e
			}
			q := tx.Model(&identity.User{}).Where("id = ? AND status = ?", actor.ID, identity.StatusActive).UpdateColumn("role", identity.PlatRoleSuperadmin)
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return errPhaseHarness
			}
			seed.Actors[i] = phaseNum(actor.ID)
		}
		ciphertext, e := h.s.Keyring.Encrypt(h.e.token)
		if e != nil {
			return e
		}
		channel := relay.Channel{Name: "Synthetic phase handoff", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", CredentialEnc: ciphertext, Models: ark.Model20, Weight: 1, Status: 1, MaxConcurrentTasks: 100}
		if e = tx.Create(&channel).Error; e != nil {
			return e
		}
		seed.ChannelID = phaseNum(channel.ID)
		scope := ProviderScope{ScopeKey: "phase-" + h.e.run, AccountRef: "synthetic-phase-handoff", Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "phase-handoff-v1", FXObservedAt: now}
		if e = tx.Create(&scope).Error; e != nil {
			return e
		}
		seed.ScopeID = phaseNum(scope.ID)
		limit := decimal.NewFromInt(1000000)
		for _, policy := range []videobilling.QuotaPolicy{
			{Scope: videobilling.ScopeProvider, OwnerID: scope.ID, Metric: videobilling.MetricCNY, Period: videobilling.PeriodLifetime, Timezone: "Asia/Shanghai", LimitAmount: &limit, Version: "phase-handoff-v1", Enabled: true},
			{OrgID: org.ID, Scope: videobilling.ScopeOrg, OwnerID: org.ID, Metric: videobilling.MetricUSD, Period: videobilling.PeriodLifetime, Timezone: "Asia/Shanghai", LimitAmount: &limit, Version: "phase-handoff-v1", Enabled: true},
		} {
			if e = tx.Create(&policy).Error; e != nil {
				return e
			}
		}
		capability := ark.Capability{PublicModel: ark.Model20, UpstreamModel: "doubao-seedance-2-0-260128", Version: "phase-handoff-v1", Enabled: true, Verified: true, Resolutions: []string{"720p"}, Ratios: []string{"16:9"}, MinDuration: 4, MaxDuration: 15, DefaultDuration: 5, DefaultResolution: "720p", DefaultRatio: "16:9", DefaultSeed: -1, DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24, MaxContentItems: 4}
		raw, e := json.Marshal(capability)
		if e != nil {
			return e
		}
		if e = tx.Create(&ModelConfig{PublicModel: ark.Model20, Version: "phase-handoff-v1", ProviderScopeID: scope.ID, ChannelID: channel.ID, Enabled: true, CapabilityJSON: string(raw), PricingVersion: "phase-handoff-v1", NoChargeStatuses: "failed,cancelled", BoundsJSON: `{"720p/16:9":{"width":1280,"height":720}}`, VerifiedAt: &now, UpdatedAt: now}).Error; e != nil {
			return e
		}
		if e = tx.Create(&videobilling.PricingRule{RuleID: "phase-handoff-v1", Version: "phase-handoff-v1", PricingKey: videobilling.PricingKey{ProviderScopeID: scope.ID, Model: ark.Model20, Mode: "generate", Resolution: "720p", ServiceTier: "default"}, RetailUSDPer1M: decimal.NewFromInt(8), SupplierCNYPer1M: decimal.NewFromInt(46), RiskMultiplier: decimal.NewFromInt(1), FormulaVersion: "phase-handoff-v1", SourceHash: strings.Repeat("a", 64), EffectiveFrom: now.Add(-time.Hour), Enabled: true}).Error; e != nil {
			return e
		}
		return tx.Create(&Policy{OrgID: org.ID, AsyncExecutionAllowed: true, CaptureHistory: true, RetentionDays: 30, UpdatedAt: now}).Error
	})
	if err != nil {
		return err
	}
	h.seed = seed
	return h.saveSeed()
}

func phaseCreateBody(handle string, changed bool) []byte {
	text := "Synthetic phase " + handle
	if changed {
		text += " different"
	}
	raw, _ := json.Marshal(map[string]any{"model": ark.Model20, "content": []any{map[string]any{"type": "text", "text": text}}, "duration": 5, "resolution": "720p", "ratio": "16:9"})
	return raw
}
func (h *phaseHarness) rememberCreated(handle, id string, body []byte, idem string) error {
	org, e := phaseID(h.seed.OrgID)
	if e != nil {
		return e
	}
	var st State
	var op Operation
	if e = h.verify.Where("org_id = ? AND task_id = ?", org, id).Take(&st).Error; e != nil {
		return e
	}
	if e = h.verify.Where("org_id = ? AND resource_id = ? AND kind = ?", org, id, "create_task").Take(&op).Error; e != nil {
		return e
	}
	sum := sha256.Sum256(body)
	h.seed.Tasks[handle] = phaseSeedTask{TaskID: id, OperationID: op.ID, CreateRequestID: st.CreateRequestID, BodySHA: hex.EncodeToString(sum[:]), IdempotencyKey: idem}
	return h.refreshSeed()
}
func (h *phaseHarness) refreshSeed() error {
	if h.seed.Protocol == "" {
		return nil
	}
	org, e := phaseID(h.seed.OrgID)
	if e != nil {
		return e
	}
	for handle, item := range h.seed.Tasks {
		var row task.Task
		if e = h.verify.Where("org_id = ? AND task_id = ?", org, item.TaskID).Take(&row).Error; e != nil {
			return e
		}
		item.UpstreamID = row.UpstreamTaskID
		var receipts []QueryReceipt
		if e = h.verify.Where("org_id = ? AND task_id = ?", org, item.TaskID).Order("sequence DESC").Limit(1).Find(&receipts).Error; e != nil {
			return e
		}
		if len(receipts) == 1 {
			item.QueryAttemptID = receipts[0].AttemptID
		}
		h.seed.Tasks[handle] = item
	}
	return h.saveSeed()
}
