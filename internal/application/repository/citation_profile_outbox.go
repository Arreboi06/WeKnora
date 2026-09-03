package repository

import (
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

func markCitationEventOutboxDelivered(tx *gorm.DB, event *types.CitationProfileEvent, at time.Time) error {
	if event == nil {
		return nil
	}
	return tx.Model(&types.CitationProfileEventOutbox{}).
		Where(
			"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ? AND status <> ?",
			event.TenantID,
			event.SubjectID,
			event.KnowledgeBaseID,
			event.SubjectEpoch,
			event.ScopeID,
			event.ID,
			types.CitationProfileOutboxStatusDelivered,
		).
		Updates(map[string]interface{}{
			"status":       types.CitationProfileOutboxStatusDelivered,
			"delivered_at": at,
			"updated_at":   at,
		}).Error
}
