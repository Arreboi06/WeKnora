package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

type workbenchArtifactManager interface {
	Publish(ctx context.Context, input service.PublishWorkbenchArtifactInput) (*types.WorkbenchArtifactVersion, error)
	Get(ctx context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*types.WorkbenchArtifactVersion, error)
	Download(ctx context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*service.WorkbenchArtifactDownload, error)
	Preview(ctx context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*service.WorkbenchArtifactPreview, error)
}

type publishWorkbenchArtifactRequest struct {
	workbenchFileScopeRequest
	SourceRef  types.WorkbenchFileRef `json:"source_ref"`
	ArtifactID string                 `json:"artifact_id"`
	Version    int                    `json:"version"`
	CommandID  string                 `json:"command_id"`
	MessageID  string                 `json:"message_id"`
	SkillRunID string                 `json:"skill_run_id"`
}

func (h *WorkbenchHandler) PublishArtifact(c *gin.Context) {
	ctx, tenantID, actorID, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if !h.requireWorkbenchArtifacts(c) {
		return
	}
	var req publishWorkbenchArtifactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	artifact, err := h.artifacts.Publish(ctx, service.PublishWorkbenchArtifactInput{
		Scope:      req.scope(tenantID, session.ID),
		SourceRef:  req.SourceRef,
		ArtifactID: req.ArtifactID,
		Version:    req.Version,
		CommandID:  req.CommandID,
		MessageID:  req.MessageID,
		SkillRunID: req.SkillRunID,
		ActorID:    actorID,
	})
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	writeWorkbenchData(c, http.StatusCreated, workbenchArtifactResponse(artifact))
}

func (h *WorkbenchHandler) GetArtifact(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if !h.requireWorkbenchArtifacts(c) {
		return
	}
	artifactID, version, ok := workbenchArtifactParams(c)
	if !ok {
		return
	}
	artifact, err := h.artifacts.Get(ctx, tenantID, session.ID, artifactID, version)
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	if !h.requireCurrentArtifact(c, ctx, tenantID, session.ID, artifact) {
		return
	}
	writeWorkbenchData(c, http.StatusOK, workbenchArtifactResponse(artifact))
}

func (h *WorkbenchHandler) DownloadArtifact(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if !h.requireWorkbenchArtifacts(c) {
		return
	}
	artifactID, version, ok := workbenchArtifactParams(c)
	if !ok {
		return
	}
	download, err := h.artifacts.Download(ctx, tenantID, session.ID, artifactID, version)
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	if download == nil || !h.requireCurrentArtifact(c, ctx, tenantID, session.ID, download.Artifact) {
		return
	}
	h.writeArtifactBytes(c, download.Artifact, download.ContentType, download.Content, true)
}

func (h *WorkbenchHandler) PreviewArtifact(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if !h.requireWorkbenchArtifacts(c) {
		return
	}
	artifactID, version, ok := workbenchArtifactParams(c)
	if !ok {
		return
	}
	preview, err := h.artifacts.Preview(ctx, tenantID, session.ID, artifactID, version)
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	if preview == nil || !h.requireCurrentArtifact(c, ctx, tenantID, session.ID, preview.Artifact) {
		return
	}
	h.writeArtifactBytes(c, preview.Artifact, preview.ContentType, preview.Content, false)
}

func (h *WorkbenchHandler) requireCurrentArtifact(c *gin.Context, ctx context.Context, tenantID uint64, chatSessionID string, artifact *types.WorkbenchArtifactVersion) bool {
	if h == nil || h.workbenches == nil || artifact == nil || artifact.TenantID != tenantID || artifact.ChatSessionID != chatSessionID {
		writeWorkbenchError(c, http.StatusNotFound, workbenchErrorNotFound, "workbench artifact not found")
		return false
	}
	active, err := h.workbenches.GetActiveByChatSession(ctx, tenantID, chatSessionID)
	if err != nil {
		h.writeMappedError(c, err)
		return false
	}
	if active == nil || active.ID != artifact.WorkbenchSessionID {
		writeWorkbenchError(c, http.StatusNotFound, workbenchErrorNotFound, "workbench artifact not found")
		return false
	}
	jobID := strings.TrimSpace(artifact.WorkbenchJobID)
	if jobID == "" || h.control == nil {
		writeWorkbenchError(c, http.StatusNotFound, workbenchErrorNotFound, "workbench artifact not found")
		return false
	}
	job, err := h.control.GetJob(ctx, tenantID, jobID)
	if err != nil {
		h.writeMappedError(c, err)
		return false
	}
	if job == nil || job.TenantID != tenantID || job.ChatSessionID != chatSessionID ||
		job.WorkbenchSessionID != active.ID || job.IncarnationID != active.IncarnationID ||
		job.LeaseEpoch != active.LeaseEpoch {
		writeWorkbenchError(c, http.StatusNotFound, workbenchErrorNotFound, "workbench artifact not found")
		return false
	}
	return true
}
func (h *WorkbenchHandler) requireWorkbenchArtifacts(c *gin.Context) bool {
	if h.artifacts == nil {
		writeWorkbenchError(c, http.StatusServiceUnavailable, workbenchErrorRunnerUnavailable, "workbench artifact service is unavailable")
		return false
	}
	return true
}

func (h *WorkbenchHandler) writeArtifactBytes(c *gin.Context, artifact *types.WorkbenchArtifactVersion, contentType string, content []byte, attachment bool) {
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Cache-Control", "no-store")
	c.Header("Cross-Origin-Opener-Policy", "same-origin")
	c.Header("Cross-Origin-Resource-Policy", "same-site")
	if attachment {
		name := "artifact"
		if artifact != nil && strings.TrimSpace(artifact.FileName) != "" {
			name = strings.TrimSpace(artifact.FileName)
		}
		c.Header("Content-Disposition", workbenchAttachmentHeader(name))
	} else {
		c.Header("Content-Security-Policy", workbenchPreviewCSP(artifact))
	}
	c.Data(http.StatusOK, contentType, content)
}

func workbenchArtifactParams(c *gin.Context) (string, int, bool) {
	artifactID := strings.TrimSpace(c.Param("artifact_id"))
	version, err := strconv.Atoi(strings.TrimSpace(c.Param("version")))
	if artifactID == "" || err != nil || version <= 0 {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "invalid artifact version")
		return "", 0, false
	}
	return artifactID, version, true
}

func workbenchArtifactResponse(artifact *types.WorkbenchArtifactVersion) gin.H {
	if artifact == nil {
		return gin.H{}
	}
	return gin.H{
		"artifact_id":       artifact.ArtifactID,
		"version":           artifact.Version,
		"chat_session_id":   artifact.ChatSessionID,
		"workbench_id":      artifact.WorkbenchSessionID,
		"job_id":            artifact.WorkbenchJobID,
		"command_id":        artifact.CommandID,
		"message_id":        artifact.MessageID,
		"skill_run_id":      artifact.SkillRunID,
		"source_file_ref":   artifact.SourceFileRef,
		"content_sha256":    artifact.ContentSHA256,
		"size_bytes":        artifact.SizeBytes,
		"mime_type":         artifact.MimeType,
		"preview_class":     artifact.PreviewClass,
		"file_name":         artifact.FileName,
		"created_by":        artifact.CreatedBy,
		"created_at":        artifact.CreatedAt,
		"download_endpoint": "/api/v1/sessions/" + artifact.ChatSessionID + "/workbench/artifacts/" + artifact.ArtifactID + "/versions/" + strconv.Itoa(artifact.Version) + "/download",
		"preview_endpoint":  "/api/v1/sessions/" + artifact.ChatSessionID + "/workbench/artifacts/" + artifact.ArtifactID + "/versions/" + strconv.Itoa(artifact.Version) + "/preview",
	}
}

func workbenchPreviewCSP(artifact *types.WorkbenchArtifactVersion) string {
	base := "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; font-src data:; media-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; manifest-src 'none'; connect-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"
	if artifact != nil && artifact.PreviewClass == types.WorkbenchPreviewClassHTMLActive {
		return base + "; script-src 'unsafe-inline'; sandbox allow-scripts"
	}
	return base + "; script-src 'none'"
}

func workbenchAttachmentHeader(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return '_'
		}
		if r > 0x7e {
			return -1
		}
		return r
	}, name)
	if strings.TrimSpace(ascii) == "" {
		ascii = "artifact"
	}
	return "attachment; filename=\"" + ascii + "\""
}
