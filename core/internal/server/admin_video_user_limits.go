package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registerVideoUserLimits is mounted below the existing named-superadmin
// group. The root router calls this alongside registerVideoConfiguration.
func (s *Server) registerVideoUserLimits(su *gin.RouterGroup) {
	su.GET("/video/orgs/:org_id/users/:user_id/limits", s.getVideoUserLimits)
	su.PUT("/video/orgs/:org_id/users/:user_id/limits", s.putVideoUserLimits)
}

type videoUserLimitPut struct {
	ExpectedVersion  *uint64 `json:"expected_version"`
	CreateTaskRPM    *int    `json:"create_task_rpm"`
	MaxInflightTasks *int    `json:"max_inflight_tasks"`
	DailyTaskLimit   *int64  `json:"daily_task_limit"`
	MonthlyTaskLimit *int64  `json:"monthly_task_limit"`
	Timezone         *string `json:"timezone"`
	DailyUSDLimit    *string `json:"daily_usd_limit"`
	MonthlyUSDLimit  *string `json:"monthly_usd_limit"`
}

func userLimitIDs(c *gin.Context) (uint64, uint64, bool) {
	org, e1 := strconv.ParseUint(c.Param("org_id"), 10, 64)
	user, e2 := strconv.ParseUint(c.Param("user_id"), 10, 64)
	return org, user, e1 == nil && e2 == nil && org > 0 && user > 0
}

func parseUserBudget(raw *string) (*decimal.Decimal, error) {
	if raw == nil { // omitted/null means unlimited on this full replacement
		return nil, nil
	}
	value, err := decimal.NewFromString(*raw)
	if err != nil || value.IsNegative() || !value.Equal(value.Truncate(8)) {
		return nil, video.ErrInvalid
	}
	return &value, nil
}

func (s *Server) getVideoUserLimits(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	org, user, ok := userLimitIDs(c)
	if !ok {
		fail(c, http.StatusBadRequest, "invalid organization or user")
		return
	}
	var limit video.UserTaskLimit
	var policies []videobilling.QuotaPolicy
	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("org_id = ? AND user_id = ?", org, user).Take(&limit).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Where("org_id = ? AND scope = ? AND owner_id = ? AND model = '' AND metric = ? AND enabled = ? AND period IN ?", org, videobilling.ScopeUser, user, videobilling.MetricUSD, true, []string{videobilling.PeriodDay, videobilling.PeriodMonth}).Order("period, id DESC").Find(&policies).Error
	})
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "user limits unavailable")
		return
	}
	var daily, monthly *decimal.Decimal
	for _, p := range policies {
		if p.Period == videobilling.PeriodDay {
			daily = p.LimitAmount
		} else if p.Period == videobilling.PeriodMonth {
			monthly = p.LimitAmount
		}
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"configured": limit.Version > 0, "limits": limit, "daily_usd_limit": daily, "monthly_usd_limit": monthly})
}

func (s *Server) putVideoUserLimits(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	org, user, ok := userLimitIDs(c)
	if !ok {
		fail(c, http.StatusBadRequest, "invalid organization or user")
		return
	}
	var input videoUserLimitPut
	if videoAdminDecode(c, &input) != nil || input.ExpectedVersion == nil || input.CreateTaskRPM == nil || input.MaxInflightTasks == nil || input.DailyTaskLimit == nil || input.MonthlyTaskLimit == nil || input.Timezone == nil || *input.CreateTaskRPM < 0 || *input.CreateTaskRPM > 60000 || *input.MaxInflightTasks < 0 || *input.MaxInflightTasks > 100000 || *input.DailyTaskLimit < 0 || *input.DailyTaskLimit > 1000000000 || *input.MonthlyTaskLimit < 0 || *input.MonthlyTaskLimit > 1000000000 {
		fail(c, http.StatusBadRequest, "invalid user task limits")
		return
	}
	if len(*input.Timezone) == 0 || len(*input.Timezone) > 64 {
		fail(c, http.StatusBadRequest, "invalid timezone")
		return
	}
	if _, err := time.LoadLocation(*input.Timezone); err != nil {
		fail(c, http.StatusBadRequest, "invalid timezone")
		return
	}
	daily, err := parseUserBudget(input.DailyUSDLimit)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid daily USD limit")
		return
	}
	monthly, err := parseUserBudget(input.MonthlyUSDLimit)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid monthly USD limit")
		return
	}
	var saved video.UserTaskLimit
	conflict := false
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var owner identity.Organization
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", org, identity.StatusActive).Take(&owner).Error; e != nil {
			return e
		}
		var member identity.OrgMember
		if e := tx.Where("org_id = ? AND user_id = ?", org, user).Take(&member).Error; e != nil {
			return e
		}
		var prior video.UserTaskLimit
		e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND user_id = ?", org, user).Take(&prior).Error
		found := e == nil
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if found && (prior.Version != *input.ExpectedVersion || prior.Timezone != *input.Timezone) || !found && *input.ExpectedVersion != 0 {
			conflict = true
			return video.ErrConflict
		}
		version := uint64(1)
		if found {
			version = prior.Version + 1
			if version == 0 {
				return video.ErrInvalid
			}
		}
		now := s.now().UTC().Truncate(time.Millisecond)
		saved = video.UserTaskLimit{OrgID: org, UserID: user, CreateTaskRPM: *input.CreateTaskRPM, MaxInflightTasks: *input.MaxInflightTasks, DailyTaskLimit: *input.DailyTaskLimit, MonthlyTaskLimit: *input.MonthlyTaskLimit, Timezone: *input.Timezone, Version: version, UpdatedBy: actor.ID, UpdatedAt: now}
		if found {
			if e = tx.Model(&video.UserTaskLimit{}).Where("org_id = ? AND user_id = ? AND version = ?", org, user, prior.Version).Updates(map[string]any{"create_task_rpm": saved.CreateTaskRPM, "max_inflight_tasks": saved.MaxInflightTasks, "daily_task_limit": saved.DailyTaskLimit, "monthly_task_limit": saved.MonthlyTaskLimit, "version": version, "updated_by": actor.ID, "updated_at": now}).Error; e != nil {
				return e
			}
		} else if e = tx.Create(&saved).Error; e != nil {
			return e
		}
		// Customer USD budget policies keep the existing reserved+consumed
		// accounting and historical buckets. Revisions never reset usage.
		if e = tx.Model(&videobilling.QuotaPolicy{}).Where("org_id = ? AND scope = ? AND owner_id = ? AND model = '' AND metric = ? AND period IN ? AND enabled = ?", org, videobilling.ScopeUser, user, videobilling.MetricUSD, []string{videobilling.PeriodDay, videobilling.PeriodMonth}, true).Update("enabled", false).Error; e != nil {
			return e
		}
		for _, item := range []struct {
			period string
			amount *decimal.Decimal
		}{{videobilling.PeriodDay, daily}, {videobilling.PeriodMonth, monthly}} {
			if item.amount == nil {
				continue
			}
			p := videobilling.QuotaPolicy{OrgID: org, Scope: videobilling.ScopeUser, OwnerID: user, Model: "", Metric: videobilling.MetricUSD, Period: item.period, Timezone: saved.Timezone, LimitAmount: item.amount, Version: fmt.Sprintf("user-limits-v%d", version), Enabled: true}
			if e = tx.Create(&p).Error; e != nil {
				return e
			}
		}
		return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.user.limits.publish", Target: fmt.Sprintf("%d:%d", org, user), Detail: fmt.Sprintf("version=%d rpm=%d inflight=%d day_tasks=%d month_tasks=%d", version, saved.CreateTaskRPM, saved.MaxInflightTasks, saved.DailyTaskLimit, saved.MonthlyTaskLimit), CreatedAt: now.UnixMilli()}).Error
	})
	if conflict || errors.Is(err, video.ErrConflict) {
		fail(c, http.StatusConflict, "user limits version or timezone conflict")
		return
	}
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "user limits publication unavailable")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"limits": saved, "daily_usd_limit": daily, "monthly_usd_limit": monthly})
}
