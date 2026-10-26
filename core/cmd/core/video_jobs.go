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
		{"task_dispatch", 2 * time.Second, service.ProcessDispatchOnce},
		{"task_recovery", 2 * time.Second, service.ProcessPersistedEvidenceOnce},
		{"assets", 5 * time.Second, service.Assets.ProcessOnce},
		{"events", 5 * time.Second, service.ProcessOutbox},
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
