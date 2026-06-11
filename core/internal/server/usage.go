package server

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
)

const (
	usageDefaultPageSize = 20
	usageMaxPageSize     = 100
)

// getUsage：GET /api/usage?page=&page_size=&model=
// 调用历史（Invocation Store 元数据）。org_id 谓词强制（红线 5）；
// 响应字段由 Invocation 的 json tag 控制——渠道归属/上游 id 等平台内部信息不外露。
func (s *Server) getUsage(c *gin.Context) {
	claims := mustClaims(c)

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", strconv.Itoa(usageDefaultPageSize)))
	if pageSize < 1 || pageSize > usageMaxPageSize {
		pageSize = usageDefaultPageSize
	}

	q := s.db.Model(&invocation.Invocation{}).Where("org_id = ?", claims.OrgID)
	if model := c.Query("model"); model != "" {
		q = q.Where("model = ?", model)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "usage query failed")
		return
	}
	var rows []invocation.Invocation
	if err := q.Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "usage query failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total": total, "page": page, "page_size": pageSize, "items": rows,
	})
}
