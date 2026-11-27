package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/laiai/laiai-next/core/internal/leader"
	"github.com/laiai/laiai-next/core/internal/server"
)

// durationUntilNextLocalHM computes the duration until the next HH:MM:00 in loc.
// If today's target has already passed or equals now, it schedules tomorrow in loc.
func durationUntilNextLocalHM(now time.Time, loc *time.Location, hour, minute int) time.Duration {
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	hour = ((hour % 24) + 24) % 24
	minute = ((minute % 60) + 60) % 60
	localNow := now.In(loc)
	target := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, loc)
	if !target.After(localNow) {
		target = target.AddDate(0, 0, 1)
	}
	return max(time.Second, target.Sub(localNow))
}

func runDailyScheduledTask(
	ctx context.Context,
	apiSrv *server.Server,
	lease *leader.Lease,
	mu *sync.Mutex,
	log *slog.Logger,
	taskName string,
	getHM func(server.LogCleanupSettings) (int, int),
	job func(context.Context),
) {
	for {
		if ctx.Err() != nil {
			return
		}
		cfg := apiSrv.LoadLogCleanupSettings()
		hour, minute := getHM(cfg)
		loc := apiSrv.SiteLocation()
		wait := durationUntilNextLocalHM(time.Now(), loc, hour, minute)
		schedCh := apiSrv.ScheduleChangedChan()
		timer := time.NewTimer(wait)

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-schedCh:
			timer.Stop()
			continue
		case <-timer.C:
		}

		if lease != nil && !lease.IsLeader() {
			continue
		}
		log.Info("daily scheduled job starting", "job", taskName, "hour", hour, "minute", minute, "tz", loc.String())
		mu.Lock()
		job(ctx)
		mu.Unlock()

		// Guard against immediate re-trigger within the same second.
		tGuard := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			tGuard.Stop()
			return
		case <-tGuard.C:
		}
	}
}

func runStartupLeaderInit(ctx context.Context, lease *leader.Lease, log *slog.Logger, fn func(context.Context)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if lease == nil || lease.IsLeader() {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
	delay := time.NewTimer(5 * time.Second)
	defer delay.Stop()
	select {
	case <-ctx.Done():
		return
	case <-delay.C:
	}
	fn(ctx)
	log.Info("startup leader backfill and self-check finished")
}
