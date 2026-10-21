package video

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/laiai/laiai-next/core/internal/notify"
	"gorm.io/gorm"
)

// ProcessOutbox atomically fans out safe terminal metadata into the existing
// notification queue. Failure keeps the originating business event pending.
func (s *Service) ProcessOutbox(ctx context.Context) error {
	var rows []Outbox
	now := s.Now().UTC()
	if err := s.DB.WithContext(ctx).Where("status = ? AND next_attempt_at <= ?", "pending", now).Order("created_at,event_id").Limit(100).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var current Outbox
			if err := locked(tx).Where("org_id = ? AND event_id = ?", row.OrgID, row.EventID).First(&current).Error; err != nil {
				return err
			}
			if current.Status != "pending" {
				return nil
			}
			var event Event
			if err := tx.Where("org_id = ? AND id = ?", row.OrgID, row.EventID).First(&event).Error; err != nil {
				return err
			}
			name := ""
			if event.FromStatus != event.ToStatus {
				if event.ToStatus == "succeeded" {
					name = notify.EventTaskCompleted
				} else if event.ToStatus == "failed" {
					name = notify.EventTaskFailed
				}
			}
			if name != "" {
				var settings notify.Settings
				e := tx.Where("org_id = ?", row.OrgID).First(&settings).Error
				if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
					return e
				}
				if e == nil && settings.EventEnabled(name) {
					body, e := json.Marshal(map[string]any{"id": event.ID, "event": name, "task_id": event.TaskID, "status": event.ToStatus, "billing_state": event.BillingState, "created_at": event.CreatedAt})
					if e != nil {
						return e
					}
					if settings.WebhookURL != "" {
						if e = tx.Create(&notify.Outbox{OrgID: row.OrgID, Event: name, Payload: string(body), Channel: notify.ChannelWebhook, Target: settings.WebhookURL, Status: notify.StatusPending, NextAttemptAt: now, CreatedAt: now}).Error; e != nil {
							return e
						}
					}
					if settings.EmailEnabled {
						var emails []string
						if e = tx.Raw("SELECT u.email FROM org_members m JOIN users u ON u.id=m.user_id WHERE m.org_id=? AND m.role='owner' AND u.status=1", row.OrgID).Scan(&emails).Error; e != nil {
							return e
						}
						if len(emails) > 0 {
							if e = tx.Create(&notify.Outbox{OrgID: row.OrgID, Event: name, Payload: string(body), Channel: notify.ChannelEmail, Target: strings.Join(emails, ","), Status: notify.StatusPending, NextAttemptAt: now, CreatedAt: now}).Error; e != nil {
								return e
							}
						}
					}
				}
			}
			return tx.Model(&Outbox{}).Where("org_id = ? AND event_id = ? AND status = ?", row.OrgID, row.EventID, "pending").Updates(map[string]any{"status": "processed", "attempts": current.Attempts + 1}).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}
