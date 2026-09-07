package types

import "time"

type WorkbenchPreviewClass string

const (
	WorkbenchPreviewClassPresentationPage WorkbenchPreviewClass = "presentation_page"
	WorkbenchPreviewClassHTMLActive       WorkbenchPreviewClass = "html_active"
	WorkbenchPreviewClassTableCSV         WorkbenchPreviewClass = "table_csv"
	WorkbenchPreviewClassDownloadOnly     WorkbenchPreviewClass = "download_only"
)

func (c WorkbenchPreviewClass) IsValid() bool {
	switch c {
	case WorkbenchPreviewClassPresentationPage, WorkbenchPreviewClassHTMLActive, WorkbenchPreviewClassTableCSV, WorkbenchPreviewClassDownloadOnly:
		return true
	default:
		return false
	}
}

type WorkbenchArtifactVersion struct {
	ID                 string                `json:"id" gorm:"column:id;primaryKey"`
	ArtifactID         string                `json:"artifact_id" gorm:"column:artifact_id"`
	Version            int                   `json:"version" gorm:"column:version"`
	TenantID           uint64                `json:"tenant_id" gorm:"column:tenant_id"`
	ChatSessionID      string                `json:"chat_session_id" gorm:"column:chat_session_id"`
	WorkbenchSessionID string                `json:"workbench_id" gorm:"column:workbench_session_id"`
	WorkbenchJobID     string                `json:"job_id" gorm:"column:workbench_job_id"`
	CommandID          string                `json:"command_id,omitempty" gorm:"column:command_id"`
	MessageID          string                `json:"message_id,omitempty" gorm:"column:message_id"`
	SkillRunID         string                `json:"skill_run_id,omitempty" gorm:"column:skill_run_id"`
	SourceFileRef      WorkbenchFileRef      `json:"source_file_ref" gorm:"column:source_file_ref;type:jsonb"`
	ContentSHA256      string                `json:"content_sha256" gorm:"column:content_sha256"`
	SizeBytes          int64                 `json:"size_bytes" gorm:"column:size_bytes"`
	MimeType           string                `json:"mime_type" gorm:"column:mime_type"`
	PreviewClass       WorkbenchPreviewClass `json:"preview_class" gorm:"column:preview_class"`
	FileName           string                `json:"file_name" gorm:"column:file_name"`
	Content            []byte                `json:"-" gorm:"column:content;type:bytea"`
	CreatedBy          string                `json:"created_by" gorm:"column:created_by"`
	CreatedAt          time.Time             `json:"created_at" gorm:"column:created_at"`
}

func (WorkbenchArtifactVersion) TableName() string { return "workbench_artifact_versions" }
