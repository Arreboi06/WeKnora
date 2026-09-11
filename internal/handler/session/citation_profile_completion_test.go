package session

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type completionCitationProfileService struct {
	interfaces.CitationProfileService
	handled bool
	err     error
}

func (s *completionCitationProfileService) CompleteAssistantMessage(
	context.Context, *types.Message,
) (bool, error) {
	return s.handled, s.err
}

func TestPersistCompletedAssistantMessageContinuesAfterCommittedResolutionFailure(t *testing.T) {
	h := &Handler{citationProfileService: &completionCitationProfileService{
		handled: true,
		err: &types.CitationProfilePostCommitError{
			Cause: errors.New("resolver temporarily unavailable"),
		},
	}}

	require.True(t, h.persistCompletedAssistantMessage(context.Background(), &types.Message{ID: "message-a"}))
}

func TestPersistCompletedAssistantMessageStopsBeforeCommitFailure(t *testing.T) {
	h := &Handler{citationProfileService: &completionCitationProfileService{
		handled: true,
		err:     errors.New("transaction rolled back"),
	}}

	require.False(t, h.persistCompletedAssistantMessage(context.Background(), &types.Message{ID: "message-a"}))
}
