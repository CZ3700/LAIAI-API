package relay

// Multi-key Channel（商用迭代指令 §3.5；设计 §4.4）：单渠道挂多个上游 key，
// 401/invalid 只禁当前 key 不误杀整个渠道。选 key 三模式 + key 级自动禁用 + 全禁降级渠道。
//
// 资金零改动：多 key 只决定"用哪把 key 打上游"，hold/capture/quota 全不碰。

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// 凭证状态。
const (
	CredentialActive   int8 = 1
	CredentialDisabled int8 = 2
)

// 选 key 模式。
const (
	CredentialModeRandom   = "random"
	CredentialModePolling  = "polling"
	CredentialModeWeighted = "weighted"
)

// ChannelCredential：渠道的一个上游 key（0034 子表 channel_credentials）。
type ChannelCredential struct {
	ID             uint64     `gorm:"primaryKey" json:"id"`
	ChannelID      uint64     `gorm:"column:channel_id" json:"channel_id"`
	CredentialEnc  string     `gorm:"column:credential_enc" json:"-"` // 密文永不出 API
	Fingerprint    string     `gorm:"column:fingerprint" json:"fingerprint"`
	Status         int8       `gorm:"column:status" json:"status"`
	DisabledReason string     `gorm:"column:disabled_reason" json:"disabled_reason,omitempty"`
	DisabledAt     *time.Time `gorm:"column:disabled_at" json:"disabled_at,omitempty"`
	LastUsedAt     *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	ModeWeight     int        `gorm:"column:mode_weight" json:"mode_weight"`
	CreatedAt      time.Time  `gorm:"column:created_at" json:"created_at"`
}

func (ChannelCredential) TableName() string { return "channel_credentials" }

// CredentialFingerprint：明文 key 的展示指纹（SHA-256 前 12 hex）。绝不可逆推明文。
func CredentialFingerprint(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])[:12]
}

// credPollCounters：polling 模式的进程内轮询计数（channelID → 原子递增）。
// 并发下 atomic 递增取模，不产生热点（指令验收）。多实例各算各的，负载仍均衡。
var credPollCounters sync.Map // uint64 -> *atomic.Uint64

func nextPoll(channelID uint64) uint64 {
	v, _ := credPollCounters.LoadOrStore(channelID, new(atomic.Uint64))
	return v.(*atomic.Uint64).Add(1) - 1
}

// loadActiveCredentials：查某渠道的启用 key（稳定按 id 排序，供 polling 取模稳定）。
func loadActiveCredentials(db *gorm.DB, channelID uint64) []ChannelCredential {
	var creds []ChannelCredential
	_ = db.Where("channel_id = ? AND status = ?", channelID, CredentialActive).
		Order("id").Find(&creds).Error
	return creds
}

// pickCredential：按渠道模式从启用 key 列表选一个。空列表返回 nil。
func pickCredential(mode string, creds []ChannelCredential) *ChannelCredential {
	switch len(creds) {
	case 0:
		return nil
	case 1:
		return &creds[0] // lone key：省随机开销
	}
	switch mode {
	case CredentialModePolling:
		return &creds[int(nextPoll(creds[0].ChannelID)%uint64(len(creds)))]
	case CredentialModeWeighted:
		return weightedPickCredential(creds)
	default: // random（含未知模式兜底）
		return &creds[rand.Intn(len(creds))]
	}
}

// weightedPickCredential：按 mode_weight 加权随机（零/负权重按 1 平滑，防永不被选）。
func weightedPickCredential(creds []ChannelCredential) *ChannelCredential {
	total := 0
	for i := range creds {
		w := creds[i].ModeWeight
		if w <= 0 {
			w = 1
		}
		total += w
	}
	pick := rand.Intn(total)
	for i := range creds {
		w := creds[i].ModeWeight
		if w <= 0 {
			w = 1
		}
		pick -= w
		if pick < 0 {
			return &creds[i]
		}
	}
	return &creds[len(creds)-1]
}

// selectCredentialFrom：给定启用 key 列表选一个 → (credID, 密文, ok)。空列表返回 ok=false
// （不 fallback——legacy 回退由 legacyCredential 单独判定，避免"全禁用"被误当"无子表行"）。
func selectCredentialFrom(ch *Channel, creds []ChannelCredential) (uint64, string, bool) {
	if c := pickCredential(ch.CredentialMode, creds); c != nil {
		return c.ID, c.CredentialEnc, true
	}
	return 0, "", false
}

// legacyCredential：仅当渠道**从无子表行**（真 legacy，迁移前/未覆盖）才回退 channels.credential_enc。
// 有子表行但全禁用 → 绝不复活被禁 key（对抗 review：否则 key 级禁用这个安全控制被 credential_enc
// 镜像绕过）。credID=0 标记 legacy（key 级禁用不适用）。
func legacyCredential(db *gorm.DB, ch *Channel) (uint64, string, bool) {
	var total int64
	db.Model(&ChannelCredential{}).Where("channel_id = ?", ch.ID).Count(&total)
	if total == 0 && ch.CredentialEnc != "" {
		return 0, ch.CredentialEnc, true
	}
	return 0, "", false
}

// SelectCredential：导出的选 key（供 task/probe 等非 relay-cache 路径）。每次查 DB（低 QPS）。
func SelectCredential(db *gorm.DB, ch *Channel) (uint64, string, bool) {
	if id, enc, ok := selectCredentialFrom(ch, loadActiveCredentials(db, ch.ID)); ok {
		return id, enc, ok
	}
	return legacyCredential(db, ch)
}

// CredentialByID：按 id 取某 key 的密文（任务轮询用——固定用提交时那把 key，不受后续禁用影响）。
// 返回该 key 密文；不存在返回 ok=false。
func CredentialByID(db *gorm.DB, id uint64) (string, bool) {
	var cred ChannelCredential
	if err := db.Select("credential_enc").First(&cred, id).Error; err != nil {
		return "", false
	}
	return cred.CredentialEnc, true
}

// selectCredential：relay 热路径选 key。快照已填 ch.Credentials 则零查询；否则回退查库。
func (r *Relay) selectCredential(ch *Channel) (uint64, string, bool) {
	creds := ch.Credentials
	if creds == nil {
		creds = loadActiveCredentials(r.DB, ch.ID)
	}
	if id, enc, ok := selectCredentialFrom(ch, creds); ok {
		return id, enc, ok
	}
	return legacyCredential(r.DB, ch)
}

// ---- key 级失败计数与自动禁用（对齐 channel auto-disable 的开关+阈值）----

// noteKeyFailure：一次"应禁用"级失败路由。credID=0（legacy 单 key）→ 退化为 channel 级禁用；
// 否则累加该 key 的失败计数，达阈值禁 key，禁后若渠道无 active key 再禁 channel。受总开关约束。
func (r *Relay) noteKeyFailure(credID, channelID uint64, reason string) {
	if credID == 0 {
		r.noteFailure(channelID, reason) // legacy 单 key：与旧行为一致（channel 级）
		return
	}
	enabled, threshold := r.autoDisablePolicy()
	if !enabled {
		return // 运营关闭自动禁用：坏 key 交人工；轮换仍会时不时避开它
	}
	r.keyFailMu.Lock()
	r.keyFailCounts[credID]++
	n := r.keyFailCounts[credID]
	r.keyFailMu.Unlock()
	if n >= threshold {
		r.disableKey(credID, channelID, reason)
	}
}

// resetKeyFailures：某 key 一次成功 → 清其失败计数（与 channel resetFailures 同哲学）。
func (r *Relay) resetKeyFailures(credID uint64) {
	if credID == 0 {
		return
	}
	r.keyFailMu.Lock()
	delete(r.keyFailCounts, credID)
	r.keyFailMu.Unlock()
}

// disableKey：异步禁用一个 key；若禁后渠道再无 active key，连带禁 channel。
// 禁用的 key 不自动恢复（坏 key 恒坏，恢复靠 admin）——与 channel 的成功即恢复不同。
func (r *Relay) disableKey(credID, channelID uint64, reason string) {
	go func() {
		res := r.DB.Model(&ChannelCredential{}).
			Where("id = ? AND status = ?", credID, CredentialActive).
			Updates(map[string]any{
				"status": CredentialDisabled, "disabled_reason": clipReason(reason, 255),
				"disabled_at": r.Now(),
			})
		if res.Error != nil {
			r.Log.Error("key auto-disable failed", "cred", credID, "err", res.Error)
			return
		}
		if res.RowsAffected == 0 {
			return // 已被并发禁用
		}
		r.Log.Warn("channel key auto-disabled", "cred", credID, "channel", channelID, "reason", reason)
		var active int64
		r.DB.Model(&ChannelCredential{}).
			Where("channel_id = ? AND status = ?", channelID, CredentialActive).Count(&active)
		if active == 0 {
			r.disableChannel(channelID, "all channel keys disabled: "+reason)
		}
		r.broadcastInvalidate(KindChannel, "") // 快照失效广播：下次加载不含被禁 key
	}()
}

// clipReason：按字节截断，去尾部半个 rune（utf8mb4 列防非法 UTF-8）。
func clipReason(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxBytes], "")
}
