package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/laiai/laiai-next/core/internal/video"
)

// registerVideoJobs uses separately tracked loops so an upstream call cannot
// prevent the local persisted-evidence consumer from making progress. Each
// iteration remains synchronous on its mainJobs stack and joins at shutdown.
func registerVideoJobs(jobs *mainJobs, service *video.Service, isLeader func() bool, log *slog.Logger) {
	videoJobs := []struct {
		name     string
		interval time.Duration
		run      func(context.Context) error
	}{
		{"task_dispatch", 2 * time.Second, service.ProcessOperationsOnce},
		{"task_query", 2 * time.Second, service.ProcessQueryOnce},
		{"task_recovery", 2 * time.Second, service.ProcessPersistedEvidenceOnce},
		{"task_settlement", 2 * time.Second, service.ProcessSettlementOnce},
		{"assets", 5 * time.Second, service.Assets.ProcessOnce},
		{"events", 5 * time.Second, service.ProcessOutbox},
		{"ark_callbacks", 2 * time.Second, service.ProcessCallbackHints},
		{"stale_pending_usage", 10 * time.Minute, service.ReleaseStalePendingUsage},
		{"provider_model_quota_sync", 5 * time.Minute, func(ctx context.Context) error {
			failures := service.RefreshKnownProviderModelRateLimits(ctx)
			if len(failures) > 0 && ctx.Err() == nil {
				// Management credential and provider response details never enter logs.
				log.Warn("video provider quota refresh deferred", "accounts_failed", len(failures))
			}
			return nil
		}},
		{"evidence_retention", time.Minute, func(ctx context.Context) error {
			if _, err := service.Evidence.PurgeExpired(ctx, 100); err != nil {
				return err
			}
			return service.RecoverRequestLogs(time.Now().UTC())
		}},
	}
	for _, job := range videoJobs {
		jobs.goRun(func(ctx context.Context) {
			periodic(ctx, job.interval, func(ctx context.Context) {
				if isLeader() {
					if err := job.run(ctx); err != nil && ctx.Err() == nil {
						log.Warn("video background job deferred", "job", job.name)
					}
				}
			})
		})
	}
}
