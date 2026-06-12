package relay

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
)

// GET /v1/models（OpenAI 兼容必备端点——LangChain/SDK 工具链普遍依赖）。
// 口径：已定价 且 至少有一个活跃渠道服务 的模型才对外可见（没渠道的模型列出来只会 503）。
func (r *Relay) HandleListModels(c *gin.Context) {
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		relayErr(c, http.StatusUnauthorized, "missing api key")
		return
	}
	if _, err := identity.VerifyKey(r.DB, strings.TrimPrefix(auth, "Bearer "), r.Now()); err != nil {
		relayErr(c, http.StatusUnauthorized, "invalid api key")
		return
	}

	var prices []ModelPrice
	if err := r.DB.Order("model").Find(&prices).Error; err != nil {
		relayErr(c, http.StatusInternalServerError, "model list failed")
		return
	}
	var channels []Channel
	if err := r.DB.Where("status = 1").Find(&channels).Error; err != nil {
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
