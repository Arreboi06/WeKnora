package types

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	CitationProfileContractVersion = "t4-p36-contract-v1"
	CitationProfileCursorSchemaV1  = "citation_profile_cursor_v1"

	CitationProfileGuidanceKindNone                   = "none"
	CitationProfileGuidanceReasonEvidenceInsufficient = "evidence_insufficient"

	CitationProfileEmptyFeatureDisabled = "feature_disabled"
	CitationProfileEmptyNotEnrolled     = "not_enrolled"
	CitationProfileEmptyNoEvents        = "no_events"
	CitationProfileEmptyACLUnknown      = "acl_unknown"
	CitationProfileMessageDisabled      = "citation_profile_disabled"
	CitationProfileMessageNotEnrolled   = "citation_profile_not_enrolled"
	CitationProfileMessageNoEvents      = "citation_profile_no_events"
	CitationProfileMessageACLUnknown    = "citation_profile_acl_unknown"

	CitationProfilePolicyVersion          = "profile_policy_v1"
	CitationProfileRetentionPolicyVersion = "retention_policy_v1"

	CitationProfileACLStateCurrent = "current"
	CitationProfileACLStateUnknown = "unknown"
	CitationProfileACLStateError   = "error"
	CitationProfileACLStateTimeout = "timeout"
	CitationProfileACLStateDenied  = "denied"

	CitationProfileACLAccessPathOwner      = "owner"
	CitationProfileACLAccessPathKBShare    = "kb_share"
	CitationProfileACLAccessPathAgentShare = "agent_share"
	CitationProfileFenceReasonACLDenied    = "acl_denied"
)

type CitationProfileACLDecision string

const (
	CitationProfileACLDecisionAllow   CitationProfileACLDecision = "allow"
	CitationProfileACLDecisionDeny    CitationProfileACLDecision = "deny"
	CitationProfileACLDecisionUnknown CitationProfileACLDecision = "unknown"
	CitationProfileACLDecisionError   CitationProfileACLDecision = "error"
	CitationProfileACLDecisionTimeout CitationProfileACLDecision = "timeout"
)

// CitationProfileACLAuthorityResult carries both the authority decision and
// the earliest known instant at which an ALLOW can no longer be trusted. The
// refresh runner must never cache ALLOW beyond ValidUntil. A nil ValidUntil
// means the authority did not observe a time-bounded grant; it does not relax
// the runner's normal short refresh TTL.
type CitationProfileACLAuthorityResult struct {
	Decision   CitationProfileACLDecision
	ValidUntil *time.Time
}

// CitationProfileACLBinding is derived by the KB access guard. It is not part
// of any public request DTO and therefore cannot be supplied by a client.
type CitationProfileACLBinding struct {
	PrincipalType         string
	PrincipalID           string
	AuthenticatedTenantID uint64
	APIKeyID              uint64
	AccessPath            string
	AccessPathID          string
}

func (b CitationProfileACLBinding) Normalize() CitationProfileACLBinding {
	b.PrincipalType = strings.TrimSpace(b.PrincipalType)
	b.PrincipalID = strings.TrimSpace(b.PrincipalID)
	b.AccessPath = strings.TrimSpace(b.AccessPath)
	b.AccessPathID = strings.TrimSpace(b.AccessPathID)
	return b
}

func (b CitationProfileACLBinding) Valid() bool {
	b = b.Normalize()
	if b.PrincipalType == "" || b.PrincipalID == "" || b.AuthenticatedTenantID == 0 {
		return false
	}
	switch b.AccessPath {
	case CitationProfileACLAccessPathOwner:
		return b.AccessPathID == ""
	case CitationProfileACLAccessPathKBShare, CitationProfileACLAccessPathAgentShare:
		return b.AccessPathID != ""
	default:
		return false
	}
}

func WithCitationProfileACLBinding(ctx context.Context, binding CitationProfileACLBinding) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, CitationProfileACLBindingContextKey, binding.Normalize())
}

func CitationProfileACLBindingFromContext(ctx context.Context) (CitationProfileACLBinding, bool) {
	if ctx == nil {
		return CitationProfileACLBinding{}, false
	}
	binding, ok := ctx.Value(CitationProfileACLBindingContextKey).(CitationProfileACLBinding)
	binding = binding.Normalize()
	return binding, ok && binding.Valid()
}

const (
	CitationProfileEventStatusPendingResolution = "pending_resolution"
	CitationProfileEventStatusResolved          = "resolved"
	CitationProfileEventStatusResolvedEmpty     = "resolved_empty"
	CitationProfileEventStatusFailed            = "failed"

	CitationProfileOutboxStatusPending    = "pending"
	CitationProfileOutboxStatusDelivering = "delivering"
	CitationProfileOutboxStatusDelivered  = "delivered"
	CitationProfileOutboxStatusDeadletter = "deadletter"

	CitationProfileOutboxErrorExpired          = "outbox_expired"
	CitationProfileOutboxErrorMaxAttempts      = "max_attempts"
	CitationProfileOutboxErrorScopeDeleted     = "scope_deleted"
	CitationProfileOutboxErrorResolutionFailed = "resolution_failed"
	CitationProfileOutboxErrorLeaseLost        = "lease_lost"

	CitationProfileOutboxMessageExpired          = "resolution work expired"
	CitationProfileOutboxMessageMaxAttempts      = "resolution retry budget exhausted"
	CitationProfileOutboxMessageScopeDeleted     = "scope is no longer active"
	CitationProfileOutboxMessageResolutionFailed = "resolution attempt failed"
	CitationProfileOutboxMessageLeaseLost        = "outbox lease is no longer owned"

	EvidenceRelationCurrent    = "evidenced_current"
	EvidenceRelationHistorical = "evidenced_historical"
	EvidenceRelationDisputed   = "disputed"
	EvidenceRelationUnknown    = "unknown"

	CitationCorrectionConfirmRelevant = "confirm_relevant"
	CitationCorrectionRejectMapping   = "reject_mapping"
	CitationCorrectionRetractEvent    = "retract_event"

	CitationOperationEnrollment       = "enrollment"
	CitationOperationExport           = "export"
	CitationOperationDeleteCurrentACL = "delete_current_acl"
	CitationOperationDeleteBlind      = "delete_blind"
	CitationOperationACLRecheck       = "acl_recheck"

	CitationProfileOperationStatusPreparing = "preparing"
	CitationProfileOperationStatusReady     = "ready"
	CitationProfileOperationStatusExpired   = "expired"
	CitationProfileOperationStatusRevoked   = "revoked"
	CitationProfileOperationStatusFailed    = "failed"
	CitationProfileOperationStatusAccepted  = "accepted"

	CitationProfileExportFormatJSON    = "json"
	CitationProfileExportSchemaVersion = "citation_profile_export_v1"

	CitationProfileReceiptHiddenAndFenced = "profile_hidden_and_fenced"
	CitationProfileReceiptAccepted        = "request_accepted"

	CitationProfileCursorEndpointNodes    = "nodes"
	CitationProfileCursorEndpointEvidence = "node_evidence"
	CitationProfileCursorSortIDAsc        = "id_asc"

	CitationProfileCorrectionStateNone           = "none"
	CitationProfileCorrectionStateConfirmed      = "confirmed"
	CitationProfileCorrectionStateRejected       = "rejected"
	CitationProfileCorrectionStateEventRetracted = "event_retracted"
	CitationProfileEvidenceClaimCode             = "answer_source_linked_to_page_at_resolution_time"
)

var (
	ErrCitationProfileDisabled            = errors.New("citation profile disabled")
	ErrCitationProfileUnavailable         = errors.New("citation profile temporarily unavailable")
	ErrCitationProfileInvalidRequest      = errors.New("citation profile invalid request")
	ErrCitationProfileAuthRequired        = errors.New("citation profile auth required")
	ErrCitationProfileChanged             = errors.New("citation profile changed")
	ErrCitationProfileIdempotencyConflict = errors.New("citation profile idempotency conflict")
	ErrCitationProfileQuotaExceeded       = errors.New("citation profile quota exceeded")
	ErrCitationProfileNotFound            = errors.New("citation profile not found")
	ErrCitationProfileDeleted             = errors.New("citation profile deleted")
	ErrCitationProfileOutboxLeaseLost     = errors.New("citation profile outbox lease lost")
	ErrCitationProfileOutboxExpired       = errors.New("citation profile outbox expired")
	ErrCitationProfileOutboxMaxAttempts   = errors.New("citation profile outbox max attempts")
	ErrCitationProfileCommitted           = errors.New("citation profile committed")
	ErrCitationProfileACLLeaseLost        = errors.New("citation profile ACL lease lost")
)

// CitationProfilePostCommitError reports a resolver/notification failure
// after the authoritative message and event transaction has already committed.
// Callers must continue the normal message workflow and let durable outbox
// recovery finish the asynchronous part.
type CitationProfilePostCommitError struct {
	Cause error
}

func (e *CitationProfilePostCommitError) Error() string {
	return ErrCitationProfileCommitted.Error() + "; evidence resolution queued"
}

func (e *CitationProfilePostCommitError) Unwrap() error {
	if e == nil {
		return ErrCitationProfileCommitted
	}
	return e.Cause
}

func (e *CitationProfilePostCommitError) Is(target error) bool {
	return target == ErrCitationProfileCommitted
}

// CitationProfileConfig controls the Topic 4 profile feature. The zero value is off.
type CitationProfileConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled" mapstructure:"enabled"`
}

// CitationProfileACLRuntimeState is the single database-coordinated safety
// marker. Its enabled bit is monotonic during normal application startup: the
// first enabled rollout performs one global fence, and locally disabled peers
// continue honoring that marker on permission writes.
type CitationProfileACLRuntimeState struct {
	ID                   uint8     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Enabled              bool      `gorm:"not null" json:"-"`
	TransitionGeneration uint64    `gorm:"not null" json:"-"`
	ChangedAt            time.Time `gorm:"not null" json:"-"`
	UpdatedAt            time.Time `gorm:"not null" json:"-"`
}

func (CitationProfileACLRuntimeState) TableName() string {
	return "citation_profile_acl_runtime_state"
}

// CitationProfileGuidance is fixed for P36/P30: the backend exposes evidence only.
type CitationProfileGuidance struct {
	Kind       string   `json:"kind"`
	Reason     string   `json:"reason"`
	Candidates []string `json:"candidates"`
}

// CitationProfileDefaultGuidance returns the contract-mandated no-guidance object.
func CitationProfileDefaultGuidance() CitationProfileGuidance {
	return CitationProfileGuidance{
		Kind:       CitationProfileGuidanceKindNone,
		Reason:     CitationProfileGuidanceReasonEvidenceInsufficient,
		Candidates: []string{},
	}
}

type CitationProfileEmptyState struct {
	Kind        string `json:"kind"`
	MessageCode string `json:"message_code"`
}

type CitationProfileLimits struct {
	ActiveScopes      int `json:"active_scopes"`
	Pages             int `json:"pages"`
	Events            int `json:"events"`
	Links             int `json:"links"`
	LinksPerEvent     int `json:"links_per_event"`
	PendingOperations int `json:"pending_operations"`
	ExportEvents      int `json:"export_events"`
	GraphNodes        int `json:"graph_nodes"`
	GraphEdges        int `json:"graph_edges"`
	ListPageSize      int `json:"list_page_size"`
	ACLScanScopes     int `json:"acl_scan_scopes"`
	ACLBatchPerMinute int `json:"acl_batch_per_minute"`
}

func CitationProfileDefaultLimits() CitationProfileLimits {
	return CitationProfileLimits{
		ActiveScopes:      100,
		Pages:             5000,
		Events:            1000,
		Links:             5000,
		LinksPerEvent:     100,
		PendingOperations: 100,
		ExportEvents:      1000,
		GraphNodes:        500,
		GraphEdges:        2000,
		ListPageSize:      100,
		ACLScanScopes:     100,
		ACLBatchPerMinute: 40,
	}
}

type CitationProfileScope struct {
	ID                       string     `json:"-" gorm:"type:varchar(36);primaryKey"`
	TenantID                 uint64     `json:"-" gorm:"not null;index"`
	SubjectID                string     `json:"-" gorm:"type:varchar(512);not null;index"`
	KnowledgeBaseID          string     `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index"`
	SubjectEpoch             string     `json:"subject_epoch" gorm:"type:varchar(36);not null"`
	ProfileReadVersion       uint64     `json:"-" gorm:"not null;default:0"`
	ProfilePolicyVersion     string     `json:"profile_policy_version" gorm:"type:varchar(64);not null;default:'profile_policy_v1'"`
	RetentionPolicyVersion   string     `json:"retention_policy_version" gorm:"type:varchar(64);not null;default:'retention_policy_v1'"`
	Enabled                  bool       `json:"-" gorm:"not null;default:true"`
	ActiveRunID              string     `json:"-" gorm:"type:varchar(36)"`
	MappingRevision          uint64     `json:"-" gorm:"not null;default:0"`
	SourceUniverseWatermark  string     `json:"-" gorm:"type:varchar(128);not null;default:''"`
	PendingEventCount        int        `json:"-" gorm:"not null;default:0"`
	PendingMappingCount      int        `json:"-" gorm:"not null;default:0"`
	DirtyMappingCount        int        `json:"-" gorm:"not null;default:0"`
	ACLCheckState            string     `json:"-" gorm:"type:varchar(32);not null;default:'unknown'"`
	NextACLCheckAt           *time.Time `json:"-"`
	ACLCheckLeaseUntil       *time.Time `json:"-"`
	ACLCheckLeaseToken       string     `json:"-" gorm:"type:varchar(128);not null;default:''"`
	ACLCheckedAt             *time.Time `json:"-"`
	ACLPrincipalType         string     `json:"-" gorm:"type:varchar(32);not null;default:''"`
	ACLPrincipalID           string     `json:"-" gorm:"type:varchar(512);not null;default:''"`
	ACLAuthenticatedTenantID uint64     `json:"-" gorm:"not null;default:0"`
	ACLAPIKeyID              uint64     `json:"-" gorm:"not null;default:0"`
	ACLAccessPath            string     `json:"-" gorm:"type:varchar(32);not null;default:''"`
	ACLAccessPathID          string     `json:"-" gorm:"type:varchar(128);not null;default:''"`
	ACLGeneration            uint64     `json:"-" gorm:"not null;default:0"`
	FencedAt                 *time.Time `json:"-" gorm:"index"`
	FenceReason              string     `json:"-" gorm:"type:varchar(64);not null;default:''"`
	DeleteRequestID          string     `json:"-" gorm:"type:varchar(36)"`
	DeletedAt                *time.Time `json:"-" gorm:"index"`
	CreatedAt                time.Time  `json:"-"`
	UpdatedAt                time.Time  `json:"-"`
	ACLCurrent               bool       `json:"-" gorm:"->;-:migration"`
}

// CitationProfileACLClaim is the immutable authority-check lease returned by
// the ACL queue. Apply must compare both LeaseToken and Generation before a
// result can change the scope.
type CitationProfileACLClaim struct {
	ScopeID    string
	LeaseToken string
	Generation uint64
	Scope      CitationProfileScope
}

// CitationProfileACLMutation identifies an authoritative grant mutation. Every
// non-zero field is matched with AND semantics; zero-value fields are
// wildcards. An entirely empty mutation is rejected so a caller cannot
// accidentally suspend every profile scope.
type CitationProfileACLMutation struct {
	PrincipalType         string
	PrincipalID           string
	AuthenticatedTenantID uint64
	APIKeyID              uint64
	SourceTenantID        uint64
	KnowledgeBaseID       string
	AccessPath            string
	AccessPathID          string
}

func (CitationProfileScope) TableName() string {
	return "citation_profile_scopes"
}

func (s *CitationProfileScope) Deleted() bool {
	return s != nil && s.DeletedAt != nil
}

func (s *CitationProfileScope) Suspended() bool {
	return s == nil ||
		!s.Enabled ||
		s.FencedAt != nil ||
		s.DeletedAt != nil ||
		!s.ACLCurrentAt(time.Now().UTC())
}

// ACLAuthorityBindingValid reports whether the persisted server-derived
// authority identity is complete and internally consistent. A CURRENT state
// without this binding is legacy or corrupt data and must never be readable.
func (s *CitationProfileScope) ACLAuthorityBindingValid() bool {
	if s == nil || s.TenantID == 0 || strings.TrimSpace(s.KnowledgeBaseID) == "" ||
		strings.TrimSpace(s.ACLPrincipalID) == "" || s.ACLAuthenticatedTenantID == 0 {
		return false
	}
	switch strings.TrimSpace(s.ACLPrincipalType) {
	case PrincipalWebUser:
		if s.ACLAPIKeyID != 0 {
			return false
		}
	case PrincipalAPITenant, PrincipalAPIExternalUser:
		if s.ACLAPIKeyID == 0 {
			return false
		}
	default:
		return false
	}
	switch strings.TrimSpace(s.ACLAccessPath) {
	case CitationProfileACLAccessPathOwner:
		return strings.TrimSpace(s.ACLAccessPathID) == "" && s.ACLAuthenticatedTenantID == s.TenantID
	case CitationProfileACLAccessPathKBShare:
		return strings.TrimSpace(s.ACLAccessPathID) == strings.TrimSpace(s.KnowledgeBaseID)
	case CitationProfileACLAccessPathAgentShare:
		return strings.TrimSpace(s.ACLAccessPathID) != ""
	default:
		return false
	}
}

// ACLCurrentAt is the single in-memory fail-closed gate for citation-profile
// surfaces. CURRENT is meaningful only while the server-derived authority
// proof is complete, has actually been checked, has no in-flight lease, and
// has not expired.
func (s *CitationProfileScope) ACLCurrentAt(now time.Time) bool {
	if s == nil || strings.TrimSpace(s.ACLCheckState) != CitationProfileACLStateCurrent ||
		!s.ACLAuthorityBindingValid() || s.ACLCheckedAt == nil || s.NextACLCheckAt == nil ||
		strings.TrimSpace(s.ACLCheckLeaseToken) != "" || s.ACLCheckLeaseUntil != nil || s.ACLGeneration == 0 {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	checkedAt := s.ACLCheckedAt.UTC()
	nextCheckAt := s.NextACLCheckAt.UTC()
	return !checkedAt.After(now) && nextCheckAt.After(now) && nextCheckAt.After(checkedAt)
}

// CitationProfileSnapshotCutoffs returns the three typed opaque cutoffs that
// bind the event, correction, and active-run projections to one profile
// version. The timestamp is informational; the version is the consistency
// boundary used by cursor validation.
func CitationProfileSnapshotCutoffs(readVersion uint64, stateAt time.Time) (string, string, string) {
	versionToken := "rv:" + strconv.FormatUint(readVersion, 10)
	if !stateAt.IsZero() {
		versionToken += ";at:" + stateAt.UTC().Format(time.RFC3339Nano)
	}
	return "event:" + versionToken, "correction:" + versionToken, "active_run:" + versionToken
}

func CitationProfileScopeSnapshotCutoffs(scope *CitationProfileScope) (string, string, string) {
	if scope == nil {
		return CitationProfileSnapshotCutoffs(0, time.Time{})
	}
	stateAt := scope.UpdatedAt
	if stateAt.IsZero() {
		stateAt = scope.CreatedAt
	}
	return CitationProfileSnapshotCutoffs(scope.ProfileReadVersion, stateAt)
}

type CitationProfileScopeDTO struct {
	KnowledgeBaseID        string `json:"knowledge_base_id"`
	SubjectEpoch           string `json:"subject_epoch"`
	ProfilePolicyVersion   string `json:"profile_policy_version"`
	RetentionPolicyVersion string `json:"retention_policy_version"`
}

func (s *CitationProfileScope) DTO() *CitationProfileScopeDTO {
	if s == nil {
		return nil
	}
	profilePolicy := s.ProfilePolicyVersion
	if profilePolicy == "" {
		profilePolicy = CitationProfilePolicyVersion
	}
	retentionPolicy := s.RetentionPolicyVersion
	if retentionPolicy == "" {
		retentionPolicy = CitationProfileRetentionPolicyVersion
	}
	return &CitationProfileScopeDTO{
		KnowledgeBaseID:        s.KnowledgeBaseID,
		SubjectEpoch:           s.SubjectEpoch,
		ProfilePolicyVersion:   profilePolicy,
		RetentionPolicyVersion: retentionPolicy,
	}
}

type CitationProfileSnapshot struct {
	SubjectEpoch                 string `json:"subject_epoch,omitempty"`
	ReadVersion                  string `json:"read_version"`
	EventCutoff                  string `json:"event_cutoff,omitempty"`
	CorrectionCutoff             string `json:"correction_cutoff,omitempty"`
	ActiveRunPointerCutoff       string `json:"active_run_pointer_cutoff,omitempty"`
	CurrentIndexWatermark        string `json:"current_index_watermark,omitempty"`
	CurrentWikiUniverseWatermark string `json:"current_wiki_universe_watermark,omitempty"`
	MappingRevision              string `json:"mapping_revision,omitempty"`
	SourceUniverseWatermark      string `json:"source_universe_watermark,omitempty"`
	DirtyEventCount              int    `json:"dirty_event_count,omitempty"`
	PendingEventCount            int    `json:"pending_event_count,omitempty"`
	PendingMappingCount          int    `json:"pending_mapping_count,omitempty"`
	DirtyMappingCount            int    `json:"dirty_mapping_count,omitempty"`
	CapturedAt                   string `json:"captured_at,omitempty"`
}

type CitationProfileStatus struct {
	Enabled    bool                      `json:"enabled"`
	Enrolled   bool                      `json:"enrolled"`
	Deleted    bool                      `json:"deleted"`
	Suspended  bool                      `json:"suspended"`
	Scope      *CitationProfileScopeDTO  `json:"scope"`
	Snapshot   *CitationProfileSnapshot  `json:"snapshot"`
	Guidance   CitationProfileGuidance   `json:"guidance"`
	EmptyState CitationProfileEmptyState `json:"empty_state,omitempty"`
	Limits     CitationProfileLimits     `json:"limits"`
}

type CitationProfileEnrollmentRequest struct {
	Enabled             bool   `json:"enabled"`
	ExpectedReadVersion string `json:"expected_read_version,omitempty"`
	IdempotencyKey      string `json:"idempotency_key"`
}

type CitationProfileEnrollmentResponse struct {
	Enabled  bool                     `json:"enabled"`
	Enrolled bool                     `json:"enrolled"`
	Scope    *CitationProfileScopeDTO `json:"scope"`
	Snapshot *CitationProfileSnapshot `json:"snapshot"`
}

type CitationProfileExportRequest struct {
	ExpectedReadVersion string `json:"expected_read_version"`
	IdempotencyKey      string `json:"idempotency_key"`
	Format              string `json:"format"`
}

type CitationProfileExportOperationResponse struct {
	OperationID   string                   `json:"operation_id"`
	Status        string                   `json:"status"`
	Snapshot      *CitationProfileSnapshot `json:"snapshot,omitempty"`
	ExpiresAt     string                   `json:"expires_at,omitempty"`
	DownloadURL   *string                  `json:"download_url"`
	SchemaVersion string                   `json:"schema_version,omitempty"`
}

type CitationProfileDeleteRequest struct {
	ExpectedReadVersion string `json:"expected_read_version,omitempty"`
	IdempotencyKey      string `json:"idempotency_key,omitempty"`
}

type CitationProfileDeleteResponse struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	ReceiptCode string `json:"receipt_code"`
}

type CitationProfileCursor struct {
	SchemaVersion                string `json:"schema_version"`
	Endpoint                     string `json:"endpoint"`
	KnowledgeBaseID              string `json:"kb_id"`
	SubjectEpoch                 string `json:"subject_epoch"`
	ReadVersion                  string `json:"read_version"`
	EventCutoff                  string `json:"event_cutoff,omitempty"`
	CorrectionCutoff             string `json:"correction_cutoff,omitempty"`
	ActiveRunPointerCutoff       string `json:"active_run_pointer_cutoff,omitempty"`
	CurrentIndexWatermark        string `json:"current_index_watermark,omitempty"`
	CurrentWikiUniverseWatermark string `json:"current_wiki_universe_watermark,omitempty"`
	MappingRevision              string `json:"mapping_revision,omitempty"`
	SourceUniverseWatermark      string `json:"source_universe_watermark,omitempty"`
	DirtyEventCount              int    `json:"dirty_event_count,omitempty"`
	PendingEventCount            int    `json:"pending_event_count,omitempty"`
	PendingMappingCount          int    `json:"pending_mapping_count,omitempty"`
	DirtyMappingCount            int    `json:"dirty_mapping_count,omitempty"`
	CapturedAt                   string `json:"captured_at,omitempty"`
	PageSize                     int    `json:"page_size"`
	Sort                         string `json:"sort"`
	LastPageUUID                 string `json:"last_page_uuid,omitempty"`
	LastRelationID               string `json:"last_relation_id,omitempty"`
}

type CitationProfileNodeDTO struct {
	PageUUID                string `json:"page_uuid"`
	PageVersion             string `json:"page_version"`
	Title                   string `json:"title"`
	Slug                    string `json:"slug"`
	PageType                string `json:"page_type"`
	Overlay                 string `json:"overlay"`
	AuthorizedEvidenceCount int    `json:"authorized_evidence_count"`
	CurrentLinkCount        int    `json:"current_link_count"`
	HistoricalLinkCount     int    `json:"historical_link_count"`
	DisputedLinkCount       int    `json:"disputed_link_count"`
	StaleMapping            bool   `json:"stale_mapping"`
	EvidenceHref            string `json:"evidence_href"`
	UpdatedAt               string `json:"updated_at"`
}

type CitationProfileNodeListResponse struct {
	Snapshot     *CitationProfileSnapshot `json:"snapshot"`
	Items        []CitationProfileNodeDTO `json:"items"`
	PageSize     int                      `json:"page_size"`
	NextCursor   *string                  `json:"next_cursor"`
	CompleteList bool                     `json:"complete_list"`
	Guidance     CitationProfileGuidance  `json:"guidance"`
}

type CitationProfileGraphEdgeDTO struct {
	SourcePageUUID     string `json:"source_page_uuid"`
	TargetPageUUID     string `json:"target_page_uuid"`
	EdgeType           string `json:"edge_type"`
	EvidenceEventCount int    `json:"evidence_event_count"`
	StaleMapping       bool   `json:"stale_mapping"`
}

type CitationProfileGraphCaps struct {
	MaxNodes int `json:"max_nodes"`
	MaxEdges int `json:"max_edges"`
}

type CitationProfileGraphResponse struct {
	Snapshot        *CitationProfileSnapshot      `json:"snapshot"`
	Nodes           []CitationProfileNodeDTO      `json:"nodes"`
	Edges           []CitationProfileGraphEdgeDTO `json:"edges"`
	Caps            CitationProfileGraphCaps      `json:"caps"`
	GraphTruncated  bool                          `json:"graph_truncated"`
	CompleteListURL string                        `json:"complete_list_url"`
	Guidance        CitationProfileGuidance       `json:"guidance"`
}

type CitationProfileEvidencePageDTO struct {
	PageUUID    string `json:"page_uuid"`
	PageVersion string `json:"page_version"`
	Title       string `json:"title"`
	Slug        string `json:"slug"`
}

type CitationProfileCorrectionHistoryItem struct {
	CorrectionID string `json:"correction_id"`
	Action       string `json:"action"`
	ReasonCode   string `json:"reason_code,omitempty"`
	CreatedAt    string `json:"created_at"`
}

type CitationProfileEvidenceItemDTO struct {
	EventID                      string                                 `json:"event_id"`
	RunID                        string                                 `json:"run_id"`
	RelationID                   string                                 `json:"relation_id"`
	Overlay                      string                                 `json:"overlay"`
	ClaimCode                    string                                 `json:"claim_code"`
	MessageID                    string                                 `json:"message_id"`
	OriginReferenceIndex         int                                    `json:"origin_reference_index"`
	SourceKnowledgeID            string                                 `json:"source_knowledge_id"`
	SourceResultID               string                                 `json:"source_result_id"`
	SourceChunkIndex             *int                                   `json:"source_chunk_index"`
	KnowledgeRecordVersion       string                                 `json:"knowledge_record_version"`
	AuthoritativeKnowledgeBaseID string                                 `json:"authoritative_knowledge_base_id"`
	PageUUID                     string                                 `json:"page_uuid"`
	PageVersionAtResolution      string                                 `json:"page_version_at_resolution"`
	OccurredAt                   string                                 `json:"occurred_at"`
	ResolvedAt                   string                                 `json:"resolved_at"`
	RunMappingRevision           string                                 `json:"run_mapping_revision"`
	RunUniverseWatermark         string                                 `json:"run_universe_watermark"`
	StaleMapping                 bool                                   `json:"stale_mapping"`
	CorrectionState              string                                 `json:"correction_state"`
	CorrectionHistory            []CitationProfileCorrectionHistoryItem `json:"correction_history"`
}

type CitationProfileNodeEvidenceResponse struct {
	Snapshot   *CitationProfileSnapshot         `json:"snapshot"`
	Page       CitationProfileEvidencePageDTO   `json:"page"`
	Items      []CitationProfileEvidenceItemDTO `json:"items"`
	NextCursor *string                          `json:"next_cursor"`
	Guidance   CitationProfileGuidance          `json:"guidance"`
}

type CitationProfileCorrectionRequest struct {
	ExpectedReadVersion string `json:"expected_read_version"`
	IdempotencyKey      string `json:"idempotency_key"`
	Action              string `json:"action"`
	EventID             string `json:"event_id"`
	PageUUID            string `json:"page_uuid"`
	ReasonCode          string `json:"reason_code,omitempty"`
}

type CitationProfileCorrectionResponse struct {
	CorrectionID string                   `json:"correction_id"`
	Action       string                   `json:"action"`
	Snapshot     *CitationProfileSnapshot `json:"snapshot"`
	Result       string                   `json:"result"`
}
type CitationProfileEvent struct {
	ID                   string          `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID             uint64          `json:"-" gorm:"not null;index"`
	SubjectID            string          `json:"-" gorm:"type:varchar(512);not null;index"`
	KnowledgeBaseID      string          `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index"`
	SubjectEpoch         string          `json:"subject_epoch" gorm:"type:varchar(36);not null;index"`
	ScopeID              string          `json:"scope_id" gorm:"type:varchar(36);not null;index"`
	SessionID            string          `json:"session_id" gorm:"type:varchar(36);not null;default:''"`
	MessageID            string          `json:"message_id" gorm:"type:varchar(36);not null;index"`
	MessageVersion       string          `json:"message_version" gorm:"type:varchar(64);not null;default:''"`
	MessageCompletedAt   *time.Time      `json:"message_completed_at,omitempty"`
	OriginReferenceIndex int             `json:"origin_reference_index" gorm:"not null"`
	SourceKnowledgeID    string          `json:"source_knowledge_id" gorm:"type:varchar(36);not null;index"`
	SourceResultID       string          `json:"source_result_id" gorm:"type:varchar(128);not null;default:''"`
	SourceChunkIndex     *int            `json:"source_chunk_index,omitempty"`
	SourceRefRaw         string          `json:"source_ref_raw" gorm:"type:text;not null;default:''"`
	SourceRefNormalized  string          `json:"source_ref_normalized" gorm:"type:varchar(512);not null;default:''"`
	SourceRefsSnapshot   json.RawMessage `json:"source_refs_snapshot" gorm:"type:jsonb;default:'{}'"`
	KnowledgeSnapshot    json.RawMessage `json:"knowledge_snapshot" gorm:"type:jsonb;default:'{}'"`
	KnowledgeBaseProof   json.RawMessage `json:"knowledge_base_proof" gorm:"type:jsonb;default:'{}'"`
	ProducerEventKey     string          `json:"producer_event_key" gorm:"type:varchar(512);not null"`
	ContentHash          string          `json:"content_hash" gorm:"type:varchar(64);not null;default:''"`
	Status               string          `json:"status" gorm:"type:varchar(32);not null;default:'pending_resolution'"`
	ActiveRunID          string          `json:"active_run_id" gorm:"type:varchar(36)"`
	PendingReason        string          `json:"pending_reason" gorm:"type:varchar(64);not null;default:''"`
	FailedReason         string          `json:"failed_reason" gorm:"type:text;not null;default:''"`
	ResolvedAt           *time.Time      `json:"resolved_at,omitempty"`
	RetractedAt          *time.Time      `json:"retracted_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

func (CitationProfileEvent) TableName() string {
	return "citation_profile_events"
}

type CitationProfileEventOutbox struct {
	ID                       string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID                 uint64     `json:"-" gorm:"not null;index"`
	SubjectID                string     `json:"-" gorm:"type:varchar(512);not null;index"`
	KnowledgeBaseID          string     `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index"`
	SubjectEpoch             string     `json:"subject_epoch" gorm:"type:varchar(36);not null;index"`
	ScopeID                  string     `json:"scope_id" gorm:"type:varchar(36);not null;index"`
	EventID                  string     `json:"event_id" gorm:"type:varchar(36);not null;index"`
	Status                   string     `json:"status" gorm:"type:varchar(32);not null;default:'pending'"`
	AttemptCount             int        `json:"attempt_count" gorm:"not null;default:0"`
	NextAttemptAt            time.Time  `json:"next_attempt_at"`
	LockedAt                 *time.Time `json:"locked_at,omitempty"`
	LeaseUntil               *time.Time `json:"lease_until,omitempty"`
	LockedBy                 string     `json:"locked_by" gorm:"type:varchar(128);not null;default:''"`
	DeliveredAt              *time.Time `json:"delivered_at,omitempty"`
	DeadletterAt             *time.Time `json:"deadletter_at,omitempty"`
	LastErrorCode            string     `json:"last_error_code" gorm:"type:varchar(64);not null;default:''"`
	LastErrorMessage         string     `json:"last_error_message" gorm:"type:text;not null;default:''"`
	RetryBudgetPausedAt      *time.Time `json:"-"`
	RetryBudgetPausedSeconds int64      `json:"-" gorm:"not null;default:0"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

func (CitationProfileEventOutbox) TableName() string {
	return "citation_profile_event_outbox"
}

type WikiSourceRefIndex struct {
	ID                string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID          uint64    `json:"tenant_id" gorm:"not null;index;uniqueIndex:uq_wiki_source_ref_index_version,priority:1"`
	KnowledgeBaseID   string    `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index;uniqueIndex:uq_wiki_source_ref_index_version,priority:2"`
	SourceKnowledgeID string    `json:"source_knowledge_id" gorm:"type:varchar(36);not null;index;uniqueIndex:uq_wiki_source_ref_index_version,priority:3"`
	PageUUID          string    `json:"page_uuid" gorm:"type:varchar(36);not null;index;uniqueIndex:uq_wiki_source_ref_index_version,priority:4"`
	PageVersion       int       `json:"page_version" gorm:"not null;uniqueIndex:uq_wiki_source_ref_index_version,priority:5"`
	PageSlug          string    `json:"page_slug" gorm:"type:varchar(255);not null;default:''"`
	PageTitle         string    `json:"page_title" gorm:"type:varchar(512);not null;default:''"`
	NormalizedRef     string    `json:"normalized_ref" gorm:"type:varchar(512);not null;uniqueIndex:uq_wiki_source_ref_index_version,priority:6"`
	MappingRevision   uint64    `json:"mapping_revision" gorm:"not null;uniqueIndex:uq_wiki_source_ref_index_version,priority:7"`
	LifecycleState    string    `json:"lifecycle_state" gorm:"type:varchar(32);not null;default:'current';uniqueIndex:uq_wiki_source_ref_index_version,priority:8"`
	IndexWatermark    string    `json:"index_watermark" gorm:"type:varchar(128);not null;default:''"`
	IndexedAt         time.Time `json:"indexed_at"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (WikiSourceRefIndex) TableName() string {
	return "wiki_source_ref_index"
}

type EvidenceResolutionRun struct {
	ID                   string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID             uint64     `json:"-" gorm:"not null;index"`
	SubjectID            string     `json:"-" gorm:"type:varchar(512);not null;index"`
	KnowledgeBaseID      string     `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index"`
	SubjectEpoch         string     `json:"subject_epoch" gorm:"type:varchar(36);not null;index"`
	ScopeID              string     `json:"scope_id" gorm:"type:varchar(36);not null;index"`
	EventID              string     `json:"event_id" gorm:"type:varchar(36);not null;index"`
	Status               string     `json:"status" gorm:"type:varchar(32);not null"`
	RunMappingRevision   uint64     `json:"run_mapping_revision" gorm:"not null"`
	RunUniverseWatermark string     `json:"run_universe_watermark" gorm:"type:varchar(128);not null"`
	InputHash            string     `json:"input_hash" gorm:"type:varchar(64);not null;default:''"`
	OutputHash           string     `json:"output_hash" gorm:"type:varchar(64);not null;default:''"`
	InputCount           int        `json:"input_count" gorm:"not null;default:0"`
	OutputCount          int        `json:"output_count" gorm:"not null;default:0"`
	ErrorCode            string     `json:"error_code" gorm:"type:varchar(64);not null;default:''"`
	ErrorMessage         string     `json:"error_message" gorm:"type:text;not null;default:''"`
	StartedAt            time.Time  `json:"started_at"`
	ResolvedAt           *time.Time `json:"resolved_at,omitempty"`
	FailedAt             *time.Time `json:"failed_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func (EvidenceResolutionRun) TableName() string {
	return "evidence_resolution_runs"
}

type EvidenceNodeLink struct {
	ID                string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID          uint64    `json:"-" gorm:"not null;index"`
	SubjectID         string    `json:"-" gorm:"type:varchar(512);not null;index"`
	KnowledgeBaseID   string    `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index"`
	SubjectEpoch      string    `json:"subject_epoch" gorm:"type:varchar(36);not null;index"`
	ScopeID           string    `json:"scope_id" gorm:"type:varchar(36);not null;index"`
	EventID           string    `json:"event_id" gorm:"type:varchar(36);not null;index"`
	ResolutionRunID   string    `json:"resolution_run_id" gorm:"type:varchar(36);not null;index"`
	SourceKnowledgeID string    `json:"source_knowledge_id" gorm:"type:varchar(36);not null;index"`
	PageUUID          string    `json:"page_uuid" gorm:"type:varchar(36);not null;index"`
	PageVersion       int       `json:"page_version" gorm:"not null"`
	NormalizedRef     string    `json:"normalized_ref" gorm:"type:varchar(512);not null;default:''"`
	RelationState     string    `json:"relation_state" gorm:"type:varchar(32);not null;default:'evidenced_current'"`
	RelationSource    string    `json:"relation_source" gorm:"type:varchar(32);not null;default:'source_ref_index'"`
	MappingRevision   uint64    `json:"mapping_revision" gorm:"not null"`
	UniverseWatermark string    `json:"universe_watermark" gorm:"type:varchar(128);not null;default:''"`
	CreatedAt         time.Time `json:"created_at"`
}

func (EvidenceNodeLink) TableName() string {
	return "evidence_node_links"
}

type CitationProfileCorrection struct {
	ID                   string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID             uint64    `json:"-" gorm:"not null;index"`
	SubjectID            string    `json:"-" gorm:"type:varchar(512);not null;index"`
	KnowledgeBaseID      string    `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index"`
	SubjectEpoch         string    `json:"subject_epoch" gorm:"type:varchar(36);not null;index"`
	ScopeID              string    `json:"scope_id" gorm:"type:varchar(36);not null;index"`
	EventID              string    `json:"event_id" gorm:"type:varchar(36);not null;index"`
	PageUUID             string    `json:"page_uuid" gorm:"type:varchar(36);not null;default:''"`
	PageVersion          *int      `json:"page_version,omitempty"`
	CorrectionType       string    `json:"correction_type" gorm:"type:varchar(32);not null"`
	Reason               string    `json:"reason" gorm:"type:text;not null;default:''"`
	ActorID              string    `json:"actor_id" gorm:"type:varchar(512);not null"`
	ExpectedReadVersion  uint64    `json:"expected_read_version" gorm:"not null"`
	ResultingReadVersion uint64    `json:"resulting_read_version" gorm:"not null"`
	IdempotencyKey       string    `json:"idempotency_key" gorm:"type:varchar(128);not null;default:''"`
	CreatedAt            time.Time `json:"created_at"`
}

func (CitationProfileCorrection) TableName() string {
	return "citation_profile_corrections"
}

type CitationProfileOperation struct {
	ID              string          `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64          `json:"-" gorm:"not null;index"`
	SubjectID       string          `json:"-" gorm:"type:varchar(512);not null;index"`
	KnowledgeBaseID string          `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index"`
	SubjectEpoch    string          `json:"subject_epoch" gorm:"type:varchar(36);not null;index"`
	ScopeID         string          `json:"scope_id" gorm:"type:varchar(36);not null;index"`
	OperationType   string          `json:"operation_type" gorm:"type:varchar(32);not null"`
	IdempotencyKey  string          `json:"idempotency_key" gorm:"type:varchar(128);not null"`
	Status          string          `json:"status" gorm:"type:varchar(32);not null;default:'pending'"`
	RequestSnapshot json.RawMessage `json:"request_snapshot" gorm:"type:jsonb;default:'{}'"`
	ResultSummary   json.RawMessage `json:"result_summary" gorm:"type:jsonb;default:'{}'"`
	ArtifactURI     string          `json:"artifact_uri" gorm:"type:text;not null;default:''"`
	AttemptCount    int             `json:"attempt_count" gorm:"not null;default:0"`
	NextAttemptAt   time.Time       `json:"next_attempt_at"`
	LeaseUntil      *time.Time      `json:"lease_until,omitempty"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
	ErrorCode       string          `json:"error_code" gorm:"type:varchar(64);not null;default:''"`
	ErrorMessage    string          `json:"error_message" gorm:"type:text;not null;default:''"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func (CitationProfileOperation) TableName() string {
	return "citation_profile_operations"
}
