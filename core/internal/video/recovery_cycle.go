package video

import (
	"context"
	"errors"
	"time"
)

const (
	persistedRecoveryTimeout = 10 * time.Second
	recoveryLaneTimeout      = 2 * time.Second
	recoveryClockTimeout     = 200 * time.Millisecond
)

// Only the recovery coordinator marks its child context. Shared reducers keep
// their ordinary dispatch clock behavior when called outside these lanes.
type recoveryClockBudgetKey struct{}

type recoveryLaneResult struct {
	Selected int64
	Applied  int64
	Deferred int64
	Review   int64
	Noop     int64
	Failed   int64
}

// The external message is fixed. Preserve every internal cause for callers
// checking cancellation or database errors without formatting provider data.
type persistedRecoveryErrors struct{ causes []error }

func (e *persistedRecoveryErrors) Error() string { return "video persisted evidence recovery failed" }
func (e *persistedRecoveryErrors) Unwrap() []error {
	return append([]error(nil), e.causes...)
}

func safeRecoveryErrors(causes ...error) error {
	var kept []error
	for _, cause := range causes {
		if cause != nil {
			kept = append(kept, cause)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return &persistedRecoveryErrors{causes: kept}
}

type recoveryLaneKind int

const (
	recoveryCreateInbox recoveryLaneKind = iota
	recoveryMutationInbox
	recoveryLegacyOperations
	recoveryQueryReceipts
	recoveryLaneCount
)

var recoveryLaneNames = [...]string{"create_inbox", "mutation_inbox", "legacy_operations", "query_receipts"}

type recoveryLane struct {
	kind recoveryLaneKind
	run  func(context.Context) (recoveryLaneResult, error)
}

// ProcessPersistedEvidenceOnce consumes only already committed evidence and
// existing expired registrations. No lane sends a provider HTTP request.
func (s *Service) ProcessPersistedEvidenceOnce(ctx context.Context) (err error) {
	finish := s.metrics().startCycleKind(workerCycleRecovery)
	defer func() { finish(err) }()
	return s.processPersistedEvidenceOnce(ctx)
}

func (s *Service) processPersistedEvidenceOnce(ctx context.Context) error {
	return s.runRecoveryLanes(ctx, []recoveryLane{
		{recoveryCreateInbox, s.recoverResponseInboxProgress},
		{recoveryMutationInbox, s.recoverMutationResponseInboxProgress},
		{recoveryLegacyOperations, s.recoverOperationsProgress},
		{recoveryQueryReceipts, s.recoverQueryReceiptsProgress},
	})
}

func (s *Service) runRecoveryLanes(ctx context.Context, lanes []recoveryLane) error {
	round, cancel := context.WithTimeout(ctx, persistedRecoveryTimeout)
	defer cancel()
	var failures []error
	for _, lane := range lanes {
		if err := round.Err(); err != nil {
			return safeRecoveryErrors(append(failures, err)...)
		}
		laneCtx, stop := context.WithTimeout(context.WithValue(round, recoveryClockBudgetKey{}, true), recoveryLaneTimeout)
		result, err := lane.run(laneCtx)
		// A lane must not turn its own deadline into a successful cycle. Do
		// not turn explicit cancellation by stop() below into a false failure.
		if contextErr := laneCtx.Err(); contextErr != nil && !errors.Is(err, contextErr) {
			err = safeRecoveryErrors(err, contextErr)
		}
		stop()
		s.metrics().observeRecoveryLane(lane.kind, result, err)
		if err != nil {
			failures = append(failures, err)
		}
		// An ordinary lane error (including its local timeout) does not
		// consume the following lane's separate budget. Parent cancellation
		// still stops before any further work is started.
		if err := round.Err(); err != nil {
			return safeRecoveryErrors(append(failures, err)...)
		}
	}
	return safeRecoveryErrors(failures...)
}

type workerCycleKind int

const (
	workerCycleCombined workerCycleKind = iota
	workerCycleRecovery
	workerCycleDispatch
	workerCycleKindCount
)

var workerCycleNames = [...]string{"combined", "recovery", "dispatch"}

type workerCycleCounters struct{ started, completed, errors uint64 }

type WorkerCycleObservation struct {
	Kind      string `json:"kind"`
	Started   uint64 `json:"started"`
	Completed uint64 `json:"completed"`
	Errors    uint64 `json:"errors"`
}

type workerRecoveryCounters struct {
	cycles uint64
	errors uint64
	result recoveryLaneResult
}

type WorkerRecoveryLaneObservation struct {
	Kind     string `json:"kind"`
	Cycles   uint64 `json:"cycles"`
	Errors   uint64 `json:"errors"`
	Selected int64  `json:"selected"`
	Applied  int64  `json:"applied"`
	Deferred int64  `json:"deferred"`
	Review   int64  `json:"review"`
	Noop     int64  `json:"noop"`
	Failed   int64  `json:"failed"`
}

func (m *workerMetrics) startCycleKind(kind workerCycleKind) func(error) {
	started := time.Now()
	m.mu.Lock()
	m.cyclesStarted++
	m.cycleKinds[kind].started++
	m.lastStart = started.UTC()
	m.mu.Unlock()
	return func(err error) {
		finished := time.Now()
		m.mu.Lock()
		defer m.mu.Unlock()
		m.cyclesCompleted++
		m.cycleKinds[kind].completed++
		m.lastComplete = finished.UTC()
		if err != nil {
			m.cycleErrors++
			m.cycleKinds[kind].errors++
			m.lastError = finished.UTC()
		}
		m.timings[len(workerCallKinds)].observe(finished.Sub(started))
	}
}

func (m *workerMetrics) observeRecoveryLane(kind recoveryLaneKind, result recoveryLaneResult, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	counter := &m.recoveryLanes[kind]
	counter.cycles++
	if err != nil {
		counter.errors++
	}
	counter.result.Selected += result.Selected
	counter.result.Applied += result.Applied
	counter.result.Deferred += result.Deferred
	counter.result.Review += result.Review
	counter.result.Noop += result.Noop
	counter.result.Failed += result.Failed
}
