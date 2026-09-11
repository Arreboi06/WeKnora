package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

const (
	citationProfileOutboxPollInterval = 15 * time.Second
	citationProfileOutboxLease        = 2 * time.Minute
	citationProfileOutboxMaxAge       = 24 * time.Hour
	citationProfileOutboxMaxAttempts  = 8
	citationProfileOutboxBatchSize    = 25
	citationProfileOutboxResolveLimit = 20 * time.Second
	citationProfileOutboxMaxBackoff   = 5 * time.Minute
)

// CitationProfileOutboxRunSummary is an operationally useful, non-sensitive
// result for one sweep. It deliberately contains counts only, never event or
// subject identifiers.
type CitationProfileOutboxRunSummary struct {
	Claimed      int
	Resolved     int
	Retried      int
	Deadlettered int
}

// CitationProfileOutboxRunner is the durable recovery loop for event
// resolution. The database outbox is the source of truth; the loop is only a
// latency mechanism, so a process restart cannot lose committed work.
type CitationProfileOutboxRunner struct {
	enabled     bool
	repo        interfaces.CitationProfileRepository
	outbox      interfaces.CitationProfileOutboxRepository
	interval    time.Duration
	lease       time.Duration
	maxAge      time.Duration
	maxAttempts int
	batchSize   int
	workerID    string
	now         func() time.Time

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	started   atomic.Bool
	cancel    context.CancelFunc
}

// NewCitationProfileOutboxRunner constructs the runner. The zero-value feature
// configuration is intentionally off, and disabled startup does not touch the
// outbox table.
func NewCitationProfileOutboxRunner(
	config *types.CitationProfileConfig,
	repo interfaces.CitationProfileRepository,
	outbox interfaces.CitationProfileOutboxRepository,
) *CitationProfileOutboxRunner {
	enabled := config != nil && config.Enabled
	return &CitationProfileOutboxRunner{
		enabled:     enabled,
		repo:        repo,
		outbox:      outbox,
		interval:    citationProfileOutboxPollInterval,
		lease:       citationProfileOutboxLease,
		maxAge:      citationProfileOutboxMaxAge,
		maxAttempts: citationProfileOutboxMaxAttempts,
		batchSize:   citationProfileOutboxBatchSize,
		workerID:    "citation-profile-outbox-" + uuid.NewString(),
		now:         func() time.Time { return time.Now().UTC() },
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
}

// Start starts one process-local loop. Calling Start more than once is
// harmless; this matters when container wiring is assembled by different
// runtime modes.
func (r *CitationProfileOutboxRunner) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		if r.stopCh == nil {
			r.stopCh = make(chan struct{})
		}
		if r.doneCh == nil {
			r.doneCh = make(chan struct{})
		}
		if ctx == nil {
			ctx = context.Background()
		}
		loopCtx, cancel := context.WithCancel(ctx)
		r.cancel = cancel
		r.started.Store(true)
		if !r.enabled || r.repo == nil || r.outbox == nil {
			cancel()
			logger.Infof(ctx, "[CitationProfileOutbox] disabled")
			close(r.doneCh)
			return
		}
		if r.interval <= 0 {
			r.interval = citationProfileOutboxPollInterval
		}
		go r.loop(loopCtx)
	})
}

// Stop is idempotent and returns immediately when Start was never called.
func (r *CitationProfileOutboxRunner) Stop() {
	if r == nil || !r.started.Load() {
		return
	}
	r.stopOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		close(r.stopCh)
	})
	<-r.doneCh
}

func (r *CitationProfileOutboxRunner) loop(ctx context.Context) {
	defer close(r.doneCh)
	// Run once immediately so a restart repairs committed work without waiting
	// for a full poll interval.
	r.runAndLog(ctx)

	interval := r.interval
	if interval <= 0 {
		interval = citationProfileOutboxPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.runAndLog(ctx)
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		}
	}
}

func (r *CitationProfileOutboxRunner) runAndLog(ctx context.Context) {
	summary, err := r.RunOnce(ctx)
	if err != nil {
		logger.Warnf(ctx, "[CitationProfileOutbox] sweep failed: claimed=%d resolved=%d retried=%d deadlettered=%d error=%v", summary.Claimed, summary.Resolved, summary.Retried, summary.Deadlettered, err)
		return
	}
	if summary.Claimed > 0 {
		logger.Infof(ctx, "[CitationProfileOutbox] sweep complete: claimed=%d resolved=%d retried=%d deadlettered=%d", summary.Claimed, summary.Resolved, summary.Retried, summary.Deadlettered)
	}
}

// RunOnce performs one bounded sweep. It is exported for deterministic
// operational probes and does not require the background loop to be started.
func (r *CitationProfileOutboxRunner) RunOnce(ctx context.Context) (CitationProfileOutboxRunSummary, error) {
	var summary CitationProfileOutboxRunSummary
	if r == nil || !r.enabled || r.repo == nil || r.outbox == nil {
		return summary, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	clock := time.Now
	if r.now != nil {
		clock = r.now
	}
	workerID := r.workerID + ":" + uuid.NewString()
	batchSize := r.batchSize
	if batchSize <= 0 {
		batchSize = citationProfileOutboxBatchSize
	}
	lease := r.lease
	if lease <= 0 {
		lease = citationProfileOutboxLease
	}
	maxAge := r.maxAge
	if maxAge <= 0 {
		maxAge = citationProfileOutboxMaxAge
	}
	maxAttempts := r.maxAttempts
	if maxAttempts <= 0 {
		maxAttempts = citationProfileOutboxMaxAttempts
	}
	var sweepErrs []error
	for i := 0; i < batchSize; i++ {
		if err := ctx.Err(); err != nil {
			return summary, errors.Join(append(sweepErrs, err)...)
		}
		now := clock().UTC()
		// Lease only work that can start immediately. A slow earlier attempt
		// must not consume the lease or retry budget of the rest of the batch.
		claims, err := r.outbox.ClaimCitationProfileEventOutbox(ctx, workerID, 1, now, lease)
		if err != nil {
			return summary, errors.Join(append(sweepErrs, err)...)
		}
		if len(claims) == 0 {
			break
		}
		summary.Claimed++
		claim := &claims[0]
		if outboxClaimExpired(claim, now, maxAge) {
			if markErr := r.deadletterOutbox(ctx, claim, now, types.ErrCitationProfileOutboxExpired); markErr != nil {
				sweepErrs = append(sweepErrs, markErr)
			} else {
				summary.Deadlettered++
			}
			continue
		}
		if claim.AttemptCount >= maxAttempts {
			if markErr := r.deadletterOutbox(ctx, claim, now, types.ErrCitationProfileOutboxMaxAttempts); markErr != nil {
				sweepErrs = append(sweepErrs, markErr)
			} else {
				summary.Deadlettered++
			}
			continue
		}

		attempt, startErr := r.outbox.StartCitationProfileEventOutboxAttempt(ctx, claim, clock().UTC())
		if startErr != nil {
			sweepErrs = append(sweepErrs, startErr)
			continue
		}
		claim.AttemptCount = attempt
		resolveCtx, cancel := context.WithTimeout(ctx, citationProfileOutboxResolveLimit)
		_, resolveErr := r.repo.ResolveClaimedEvidenceEvent(resolveCtx, claim)
		cancel()
		now = clock().UTC()
		if resolveErr == nil {
			summary.Resolved++
			continue
		}
		if citationProfileOutboxTerminalError(resolveErr) || claim.AttemptCount >= maxAttempts {
			cause := resolveErr
			if claim.AttemptCount >= maxAttempts && !citationProfileOutboxTerminalError(resolveErr) {
				cause = types.ErrCitationProfileOutboxMaxAttempts
			}
			if markErr := r.deadletterOutbox(ctx, claim, now, cause); markErr != nil {
				sweepErrs = append(sweepErrs, markErr)
			} else {
				summary.Deadlettered++
			}
			continue
		}
		nextAttemptAt := now.Add(citationProfileOutboxBackoff(claim.AttemptCount))
		if markErr := r.retryOutbox(ctx, claim, now, nextAttemptAt, resolveErr); markErr != nil {
			sweepErrs = append(sweepErrs, markErr)
		} else {
			summary.Retried++
		}
	}
	return summary, errors.Join(sweepErrs...)
}

// outboxStateContext remains usable after a per-item resolver deadline. A
// cancelled request must not strand a delivering lease that was already
// claimed; the state transition gets a short independent budget.
func (r *CitationProfileOutboxRunner) retryOutbox(
	parent context.Context,
	claim *types.CitationProfileEventOutbox,
	now time.Time,
	nextAttemptAt time.Time,
	cause error,
) error {
	ctx, cancel := r.outboxStateContext(parent)
	defer cancel()
	return r.outbox.RetryCitationProfileEventOutbox(ctx, claim, now, nextAttemptAt, cause)
}

func (r *CitationProfileOutboxRunner) deadletterOutbox(
	parent context.Context,
	claim *types.CitationProfileEventOutbox,
	now time.Time,
	cause error,
) error {
	ctx, cancel := r.outboxStateContext(parent)
	defer cancel()
	return r.outbox.DeadletterCitationProfileEventOutbox(ctx, claim, now, cause)
}

func (r *CitationProfileOutboxRunner) outboxStateContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
}

func outboxClaimExpired(claim *types.CitationProfileEventOutbox, _ time.Time, maxAge time.Duration) bool {
	if claim == nil || claim.CreatedAt.IsZero() || claim.LockedAt == nil || claim.LockedAt.IsZero() {
		return false
	}
	now := claim.LockedAt.UTC()
	paused := time.Duration(claim.RetryBudgetPausedSeconds) * time.Second
	if claim.RetryBudgetPausedAt != nil && now.After(*claim.RetryBudgetPausedAt) {
		paused += now.Sub(*claim.RetryBudgetPausedAt)
	}
	activeAge := now.Sub(claim.CreatedAt) - paused
	return activeAge >= maxAge
}

func citationProfileOutboxTerminalError(err error) bool {
	return errors.Is(err, types.ErrCitationProfileDeleted) ||
		errors.Is(err, types.ErrCitationProfileNotFound)
}

func citationProfileOutboxBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return time.Second
	}
	delay := time.Second
	for i := 1; i < attempt && delay < citationProfileOutboxMaxBackoff; i++ {
		delay *= 2
	}
	if delay > citationProfileOutboxMaxBackoff {
		return citationProfileOutboxMaxBackoff
	}
	return delay
}
