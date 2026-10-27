package server

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type phaseAdminRaceRole struct{}

type phaseAdminWait struct {
	BlockingID   string `json:"blocking_id"`
	RequestingID string `json:"requesting_id"`
	Table        string `json:"table"`
	Index        string `json:"index"`
}

type phaseAdminRaceProof struct {
	Winner          string            `json:"winner"`
	Waits           []phaseAdminWait  `json:"waits"`
	Before          map[string]string `json:"before"`
	BeforeMoney     map[string]string `json:"before_money"`
	BlockedMoney    map[string]string `json:"blocked_money"`
	WinnerCommitted map[string]string `json:"winner_committed"`
	WinnerMoney     map[string]string `json:"winner_money"`
	After           map[string]string `json:"after"`
	AfterMoney      map[string]string `json:"after_money"`
	Admin           phaseAdminResult  `json:"admin"`
}

func phaseAdminConnection(q *gorm.DB) (uint64, error) {
	var id uint64
	err := phaseAdminClean(q).Raw("SELECT CONNECTION_ID()").Scan(&id).Error
	if err != nil {
		return 0, err
	}
	if id == 0 {
		return 0, errors.New("admin_connection_identity")
	}
	return id, nil
}

func (a *phaseAdminAdapter) observeWait(ctx context.Context, blocker, waiter uint64, table string) (phaseAdminWait, error) {
	out := phaseAdminWait{BlockingID: strconv.FormatUint(blocker, 10), RequestingID: strconv.FormatUint(waiter, 10), Table: table}
	if blocker == 0 || waiter == 0 || blocker == waiter || (table != "api_keys" && table != "tasks") {
		return out, errors.New("admin_lock_identity_invalid")
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var rows []struct{ ObjectName, IndexName string }
		err := a.db.WithContext(ctx).Raw(`SELECT l.OBJECT_NAME,l.INDEX_NAME FROM performance_schema.data_lock_waits w JOIN performance_schema.threads b ON b.THREAD_ID=w.BLOCKING_THREAD_ID JOIN performance_schema.threads r ON r.THREAD_ID=w.REQUESTING_THREAD_ID JOIN performance_schema.data_locks l ON l.ENGINE=w.ENGINE AND l.ENGINE_LOCK_ID=w.REQUESTING_ENGINE_LOCK_ID WHERE b.PROCESSLIST_ID=? AND r.PROCESSLIST_ID=? AND l.OBJECT_SCHEMA=DATABASE() AND l.OBJECT_NAME=?`, blocker, waiter, table).Scan(&rows).Error
		if err != nil {
			return out, err
		}
		if len(rows) > 0 {
			if rows[0].IndexName == "" {
				return out, errors.New("admin_lock_index_missing")
			}
			out.Index = rows[0].IndexName
			return out, nil
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-tick.C:
		}
	}
}

func phaseAdminReceiveID(ctx context.Context, c <-chan uint64) (uint64, error) {
	select {
	case n := <-c:
		if n == 0 {
			return 0, errors.New("admin_lock_connection_missing")
		}
		return n, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (a *phaseAdminAdapter) race(ctx context.Context, winner string) (proof phaseAdminRaceProof, err error) {
	proof.Winner = winner
	if (winner != "admin" && winner != "settlement") || a.initial == nil {
		return proof, errors.New("admin_race_precondition")
	}
	consumer, ok := any(a.service).(phaseSettlementConsumer)
	if !ok {
		return proof, errors.New("settlement_capability_absent")
	}
	before, err := a.readOracle(ctx)
	if err != nil {
		return proof, err
	}
	if err = before.requirePendingSeed(); err != nil {
		return proof, err
	}
	proof.Before = before.StorageHashes
	proof.BeforeMoney = before.safeMoney()
	// The original B still owns its 2-second budget. Both participants also get
	// this same bounded test window; no clock/due/phase or business row is edited.
	raceCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	keyTx := a.db.WithContext(raceCtx).Begin()
	if keyTx.Error != nil {
		return proof, keyTx.Error
	}
	keyOpen := true
	defer func() {
		if keyOpen {
			_ = keyTx.Rollback().Error
		}
	}()
	var key identity.APIKey
	if err = keyTx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND org_id=?", a.seed.KeyID, a.seed.OrgID).Take(&key).Error; err != nil {
		return proof, err
	}
	blocker, err := phaseAdminConnection(keyTx)
	if err != nil {
		return proof, err
	}
	winnerKey, loserTask, loserLocked := make(chan uint64, 1), make(chan uint64, 1), make(chan uint64, 1)
	resumeLoser := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(resumeLoser) }) }
	var winnerSeen, loserSeen, loserAfterSeen atomic.Bool
	name := "phase-admin-race-" + a.prefix
	isLock := func(q *gorm.DB, table string) bool {
		lock, ok := q.Statement.Clauses["FOR"].Expression.(clause.Locking)
		return q.Statement.Table == table && ok && lock.Strength == "UPDATE"
	}
	beforeCB := func(q *gorm.DB) {
		role, _ := q.Statement.Context.Value(phaseAdminRaceRole{}).(string)
		if role == "" {
			return
		}
		if role == winner && isLock(q, "api_keys") && winnerSeen.CompareAndSwap(false, true) {
			id, e := phaseAdminConnection(q)
			if e != nil {
				q.AddError(e)
			}
			winnerKey <- id
		}
		if role != winner && isLock(q, "tasks") && loserSeen.CompareAndSwap(false, true) {
			id, e := phaseAdminConnection(q)
			if e != nil {
				q.AddError(e)
			}
			loserTask <- id
		}
	}
	afterCB := func(q *gorm.DB) {
		role, _ := q.Statement.Context.Value(phaseAdminRaceRole{}).(string)
		if role == "" || role == winner || !isLock(q, "tasks") || q.Error != nil || !loserAfterSeen.CompareAndSwap(false, true) {
			return
		}
		row, valid := q.Statement.Dest.(*task.Task)
		if !valid || row.OrgID != a.seed.OrgID || row.TaskID != a.seed.TaskID || q.RowsAffected != 1 {
			q.AddError(errors.New("admin_loser_locked_wrong_task"))
			return
		}
		id, e := phaseAdminConnection(q)
		if e != nil {
			q.AddError(e)
		}
		loserLocked <- id
		select {
		case <-resumeLoser:
		case <-q.Statement.Context.Done():
			q.AddError(q.Statement.Context.Err())
		}
	}
	if err = a.db.Callback().Query().Before("gorm:query").Register(name+"-before", beforeCB); err != nil {
		return proof, err
	}
	if err = a.db.Callback().Query().After("gorm:query").Register(name+"-after", afterCB); err != nil {
		_ = a.db.Callback().Query().Remove(name + "-before")
		return proof, err
	}
	type outcome struct {
		role  string
		admin phaseAdminResult
		err   error
	}
	done := make(chan outcome, 2)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		release()
		if keyOpen {
			_ = keyTx.Rollback().Error
			keyOpen = false
		}
		joined := make(chan struct{})
		go func() { wg.Wait(); close(joined) }()
		select {
		case <-joined:
			if e := a.db.Callback().Query().Remove(name + "-before"); err == nil && e != nil {
				err = e
			}
			if e := a.db.Callback().Query().Remove(name + "-after"); err == nil && e != nil {
				err = e
			}
		case <-time.After(3 * time.Second):
			err = errors.New("admin_race_cleanup_join_failed")
		}
	}()
	start := func(role string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := context.WithValue(raceCtx, phaseAdminRaceRole{}, role)
			o := outcome{role: role}
			if role == "admin" {
				o.admin, o.err = a.settleInitial(c)
			} else {
				o.err = consumer.ProcessSettlementOnce(c)
			}
			done <- o
		}()
	}
	start(winner)
	winnerID, err := phaseAdminReceiveID(raceCtx, winnerKey)
	if err != nil {
		return proof, err
	}
	keyWait, err := a.observeWait(raceCtx, blocker, winnerID, "api_keys")
	if err != nil {
		return proof, err
	}
	loser := "admin"
	if winner == "admin" {
		loser = "settlement"
	}
	start(loser)
	loserID, err := phaseAdminReceiveID(raceCtx, loserTask)
	if err != nil {
		return proof, err
	}
	taskWait, err := a.observeWait(raceCtx, winnerID, loserID, "tasks")
	if err != nil {
		return proof, err
	}
	proof.Waits = []phaseAdminWait{keyWait, taskWait}
	// Snapshot reads use an independent pool connection, not either locked tx.
	blocked, err := a.readOracle(raceCtx)
	if err != nil {
		return proof, err
	}
	if err = phaseAdminSameMoney(before, blocked); err != nil {
		return proof, err
	}
	proof.BlockedMoney = blocked.safeMoney()
	if err = keyTx.Commit().Error; err != nil {
		return proof, err
	}
	keyOpen = false
	var first outcome
	select {
	case first = <-done:
	case <-raceCtx.Done():
		return proof, raceCtx.Err()
	}
	if first.err != nil {
		return proof, first.err
	}
	if first.role != winner {
		return proof, errors.New("admin_race_wrong_first_commit")
	}
	id, err := phaseAdminReceiveID(raceCtx, loserLocked)
	if err != nil {
		return proof, err
	}
	if id != loserID {
		return proof, errors.New("admin_loser_connection_changed")
	}
	middle, err := a.readOracle(raceCtx)
	if err != nil {
		return proof, err
	}
	if err = middle.require100(winner, *a.initial); err != nil {
		return proof, err
	}
	if (winner == "admin" && middle.Receipt.AppliedAt != nil) || (winner == "settlement" && middle.Receipt.AppliedAt == nil) {
		return proof, errors.New("admin_race_intermediate_receipt")
	}
	proof.WinnerCommitted = middle.StorageHashes
	proof.WinnerMoney = middle.safeMoney()
	release()
	var second outcome
	select {
	case second = <-done:
	case <-raceCtx.Done():
		return proof, raceCtx.Err()
	}
	if second.err != nil {
		return proof, second.err
	}
	if second.role != loser {
		return proof, errors.New("admin_race_duplicate_role")
	}
	proof.Admin = first.admin
	if second.role == "admin" {
		proof.Admin = second.admin
	}
	if proof.Admin.Replay != (winner == "settlement") {
		return proof, errors.New("admin_race_replay_oracle")
	}
	after, err := a.readOracle(ctx)
	if err != nil {
		return proof, err
	}
	if err = after.require100(winner, *a.initial); err != nil {
		return proof, err
	}
	if err = phaseAdminSameMoney(middle, after); err != nil {
		return proof, err
	}
	for _, k := range []string{"task", "state", "events", "outbox"} {
		if middle.StorageHashes[k] != after.StorageHashes[k] {
			return proof, errors.New("admin_loser_changed_projection_" + k)
		}
	}
	if after.Receipt.AppliedAt == nil || after.Receipt.RecoveryVersion != before.Receipt.RecoveryVersion+1 || !reflect.DeepEqual(after.Receipt.RecoveryDueAt, before.Receipt.RecoveryDueAt) || after.Receipt.SettlementEvidenceSHA256 != before.Receipt.SettlementEvidenceSHA256 || !reflect.DeepEqual(after.Receipt.GenerationAppliedAt, before.Receipt.GenerationAppliedAt) || !reflect.DeepEqual(after.Pin, before.Pin) {
		return proof, errors.New("admin_race_progress_oracle")
	}
	rid, ref := proof.Admin.RequestID, a.initial.PayloadRef
	if winner == "settlement" {
		rid, ref = a.seed.QueryAttemptID, after.Receipt.PayloadRef
	}
	if err = phaseAdminEventDelta(before, after, []string{"billing.settled"}, []string{rid}, []string{ref}); err != nil {
		return proof, err
	}
	var audits []AuditLog
	if err = a.db.WithContext(ctx).Where("action=? AND target=?", "ark_bill_settle", a.seed.TaskID).Find(&audits).Error; err != nil {
		return proof, err
	}
	if len(audits) != 1 || audits[0].Actor != fmt.Sprintf("user#%d(superadmin)", a.seed.ActorIDs[0]) {
		return proof, errors.New("admin_race_audit_cardinality")
	}
	var detail struct {
		RequestID string `json:"request_id"`
		Replay    *bool  `json:"replay"`
	}
	if err = phaseAdminDecode([]byte(audits[0].Detail), &detail); err != nil {
		return proof, err
	}
	if detail.RequestID != proof.Admin.RequestID || detail.Replay == nil || *detail.Replay != proof.Admin.Replay {
		return proof, errors.New("admin_race_audit_replay")
	}
	proof.After = after.StorageHashes
	proof.AfterMoney = after.safeMoney()
	return proof, nil
}
