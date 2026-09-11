package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

const (
	citationProfileACLRefreshInterval = time.Minute
	citationProfileACLRefreshLease    = 2 * time.Minute
	citationProfileACLRefreshTimeout  = 15 * time.Second
	citationProfileACLRefreshBatch    = 40
	citationProfileACLAllowTTL        = 5 * time.Minute
	citationProfileACLRetryDelay      = time.Minute
)

type CitationProfileACLRefreshRunSummary struct {
	Claimed   int
	Allowed   int
	Denied    int
	Suspended int
}

type CitationProfileACLRefreshRunner struct {
	enabled   bool
	authority interfaces.CitationProfileACLAuthority
	repo      interfaces.CitationProfileACLRepository
	workerID  string
	interval  time.Duration
	lease     time.Duration
	batchSize int
	now       func() time.Time

	startOnce sync.Once
	stopOnce  sync.Once
	started   atomic.Bool
	startErr  error
	stopCh    chan struct{}
	doneCh    chan struct{}
	cancel    context.CancelFunc
}

// NewCitationProfileACLRefreshRunner requires the durable feature-state marker
// in every mode, but only an enabled process needs the authority graph.
func NewCitationProfileACLRefreshRunner(
	config *types.CitationProfileConfig,
	authority interfaces.CitationProfileACLAuthority,
	repo interfaces.CitationProfileACLRepository,
) (*CitationProfileACLRefreshRunner, error) {
	enabled := config != nil && config.Enabled
	if enabled && authority == nil {
		return nil, errors.New("citation profile ACL authority is required when citation profile is enabled")
	}
	if repo == nil {
		return nil, errors.New("citation profile ACL repository is required")
	}
	return &CitationProfileACLRefreshRunner{
		enabled:   enabled,
		authority: authority,
		repo:      repo,
		workerID:  "citation-profile-acl-" + uuid.NewString(),
		interval:  citationProfileACLRefreshInterval,
		lease:     citationProfileACLRefreshLease,
		batchSize: citationProfileACLRefreshBatch,
		now:       func() time.Time { return time.Now().UTC() },
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}, nil
}

func (r *CitationProfileACLRefreshRunner) RunOnce(ctx context.Context) (CitationProfileACLRefreshRunSummary, error) {
	var summary CitationProfileACLRefreshRunSummary
	if r == nil || !r.enabled {
		return summary, nil
	}
	if r.authority == nil || r.repo == nil {
		return summary, errors.New("citation profile ACL runner dependencies are missing")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	clock := time.Now
	if r.now != nil {
		clock = r.now
	}
	batchSize := r.batchSize
	if batchSize <= 0 {
		batchSize = citationProfileACLRefreshBatch
	}
	lease := r.lease
	if lease <= 0 {
		lease = citationProfileACLRefreshLease
	}
	for i := 0; i < batchSize; i++ {
		now := clock().UTC()
		claims, err := r.repo.ClaimCitationProfileACLScopes(ctx, r.workerID, 1, now, lease)
		if err != nil {
			return summary, err
		}
		if len(claims) == 0 {
			break
		}
		summary.Claimed++
		claim := &claims[0]
		checkCtx, cancel := context.WithTimeout(ctx, citationProfileACLRefreshTimeout)
		authorityResult, checkErr := r.authority.CheckCitationProfileACL(checkCtx, &claim.Scope)
		cancel()
		decision := authorityResult.Decision
		if checkErr != nil {
			if errors.Is(checkErr, context.DeadlineExceeded) || errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
				decision = types.CitationProfileACLDecisionTimeout
			} else {
				decision = types.CitationProfileACLDecisionError
			}
		}
		if !citationProfileACLRunnerDecisionValid(decision) {
			decision = types.CitationProfileACLDecisionUnknown
		}
		appliedAt := clock().UTC()
		nextCheckAt := appliedAt.Add(citationProfileACLRetryDelay)
		if decision == types.CitationProfileACLDecisionAllow {
			// An authority horizon is an absolute database credential fact.
			// Forward it unchanged: only the repository can compare it with
			// database time and cap the resulting ALLOW interval safely.
			nextCheckAt = time.Time{}
			if authorityResult.ValidUntil != nil {
				nextCheckAt = authorityResult.ValidUntil.UTC()
			}
		}
		if decision == types.CitationProfileACLDecisionDeny {
			nextCheckAt = time.Time{}
		}
		if err := r.repo.ApplyCitationProfileACLResult(ctx, claim, decision, appliedAt, nextCheckAt); err != nil {
			return summary, fmt.Errorf("apply citation profile ACL result: %w", err)
		}
		switch decision {
		case types.CitationProfileACLDecisionAllow:
			summary.Allowed++
		case types.CitationProfileACLDecisionDeny:
			summary.Denied++
		default:
			summary.Suspended++
		}
	}
	return summary, nil
}

func citationProfileACLRunnerDecisionValid(decision types.CitationProfileACLDecision) bool {
	switch decision {
	case types.CitationProfileACLDecisionAllow,
		types.CitationProfileACLDecisionDeny,
		types.CitationProfileACLDecisionUnknown,
		types.CitationProfileACLDecisionError,
		types.CitationProfileACLDecisionTimeout:
		return true
	default:
		return false
	}
}

// PrepareStart is the synchronous fail-closed barrier used by application
// startup. The first enabled process performs the quarantine. Disabled
// processes validate the durable marker but never clear an already-enabled
// fleet state, so their permission write paths keep protecting enabled peers.
func (r *CitationProfileACLRefreshRunner) PrepareStart(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if r.repo == nil {
		return errors.New("citation profile ACL runner repository is missing")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.repo.PrepareCitationProfileACLFeatureState(ctx, r.enabled); err != nil {
		return fmt.Errorf("prepare citation profile ACL feature state before startup: %w", err)
	}
	return nil
}

func (r *CitationProfileACLRefreshRunner) Start(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.startOnce.Do(func() {
		if err := r.PrepareStart(ctx); err != nil {
			r.startErr = err
			return
		}
		r.started.Store(true)
		if !r.enabled {
			close(r.doneCh)
			return
		}
		if ctx == nil {
			ctx = context.Background()
		}
		loopCtx, cancel := context.WithCancel(ctx)
		r.cancel = cancel
		go r.loop(loopCtx)
	})
	return r.startErr
}

func (r *CitationProfileACLRefreshRunner) Stop() {
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

func (r *CitationProfileACLRefreshRunner) loop(ctx context.Context) {
	defer close(r.doneCh)
	r.runAndLog(ctx)
	interval := r.interval
	if interval <= 0 {
		interval = citationProfileACLRefreshInterval
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

func (r *CitationProfileACLRefreshRunner) runAndLog(ctx context.Context) {
	summary, err := r.RunOnce(ctx)
	if err != nil {
		logger.Warnf(ctx, "[CitationProfileACL] sweep failed after %d claims: error=%v", summary.Claimed, err)
		return
	}
	if summary.Claimed > 0 {
		logger.Infof(ctx, "[CitationProfileACL] sweep complete: claimed=%d allowed=%d denied=%d suspended=%d", summary.Claimed, summary.Allowed, summary.Denied, summary.Suspended)
	}
}
