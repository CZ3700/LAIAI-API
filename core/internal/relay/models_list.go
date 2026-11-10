package relay

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
)

// GET /v1/models（OpenAI 兼容必备端点——LangChain/SDK 工具链普遍依赖）。
// 口径：已定价 且 至少有一个活跃渠道服务 的模型才对外可见（没渠道的模型列出来只会 503）。
func (r *Relay) HandleListModels(c *gin.Context) {
	rdb := r.DB.WithContext(c.Request.Context()) // 纯读端点绑 ctx（审查 M-6）
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		relayErr(c, http.StatusUnauthorized, "missing api key")
		return
	}
	key, err := identity.VerifyKey(rdb, strings.TrimPrefix(auth, "Bearer "), r.Now())
	if err != nil {
		relayErr(c, http.StatusUnauthorized, "invalid api key")
		return
	}
	if key.ModelAccessMode == identity.ModelAccessInheritGroup {
		data := make([]gin.H, 0)
		resolver := relayCandidateResolver()
		err := rdb.Transaction(func(tx *gorm.DB) error {
			membership, err := resolver.ReadMembershipTx(c.Request.Context(), tx, routinggroup.CandidateSubjectRef{OrgID: key.OrgID, KeyID: key.ID}, r.Now().UTC())
			if err != nil {
				return err
			}
			observed, err := membership.Observation()
			if err != nil {
				return err
			}
			if observed.OrgID != key.OrgID || observed.KeyID != key.ID || key.RoutingGroupID == nil || observed.GroupID != *key.RoutingGroupID || observed.KeyPolicyVersion != key.PolicyVersion || observed.ModelAccessMode != identity.ModelAccessInheritGroup {
				return routinggroup.ErrCandidatesDenied
			}
			var prices []ModelPrice
			if err := tx.Order("model").Find(&prices).Error; err != nil {
				return err
			}
			var channels []Channel
			if err := tx.Where("status = 1").Find(&channels).Error; err != nil {
				return err
			}
			byID := make(map[uint64]Channel, len(channels))
			for _, channel := range channels {
				byID[channel.ID] = channel
			}
			for _, price := range prices {
				model, err := membership.ResolveModel(price.Model)
				if err != nil || !model.ModelIncluded {
					continue
				}
				for _, id := range model.ChannelIDs {
					if channel, ok := byID[id]; ok && channel.ServesModel(price.Model) {
						data = append(data, gin.H{"id": price.Model, "object": "model", "owned_by": channel.Provider})
						break
					}
				}
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			code, message := groupedChannelFailure(err)
			relayErr(c, code, message)
			return
		}
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
		return
	}

	var prices []ModelPrice
	if err := rdb.Order("model").Find(&prices).Error; err != nil {
		relayErr(c, http.StatusInternalServerError, "model list failed")
		return
	}
	var channels []Channel
	if err := rdb.Where("status = 1").Find(&channels).Error; err != nil {
		relayErr(c, http.StatusInternalServerError, "model list failed")
		return
	}

	// model → 服务它的第一个活跃渠道的 provider（owned_by 语义）
	servedBy := func(model string) (string, bool) {
		for i := range channels {
			if channels[i].ServesModel(model) {
				return channels[i].Provider, true
			}
		}
		return "", false
	}

	data := make([]gin.H, 0, len(prices))
	for _, p := range prices {
		provider, ok := servedBy(p.Model)
		if !ok {
			continue
		}
		data = append(data, gin.H{
			"id":       p.Model,
			"object":   "model",
			"owned_by": provider,
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}
