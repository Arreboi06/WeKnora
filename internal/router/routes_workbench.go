package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterWorkbenchRoutes registers the default-off local Workbench control routes.
func RegisterWorkbenchRoutes(r *gin.RouterGroup, h *handler.WorkbenchHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	sessions := g.apiKeyGroup(r.Group("/sessions", g.Viewer()), apiKeyChat(apiKeyFullAccess()))
	{
		sessions.POST("/:session_id/workbench/start", h.Start)
		sessions.GET("/:id/workbench", h.Get)
		sessions.POST("/:session_id/workbench/jobs", h.CreateJob)
		sessions.GET("/:id/workbench/jobs/:job_id", h.GetJob)
		sessions.POST("/:session_id/workbench/jobs/:job_id/commands", h.CreateCommand)
		sessions.POST("/:session_id/workbench/jobs/:job_id/signals", h.SignalJob)
		sessions.GET("/:id/workbench/events", h.ListEvents)
		sessions.GET("/:id/workbench/audit", h.ListAuditOutbox)
		sessions.POST("/:session_id/workbench/stream-ticket", h.CreateStreamTicket)
		sessions.GET("/:id/workbench/stream", h.Stream)
		sessions.POST("/:session_id/workbench/files/browse", h.BrowseFiles)
		sessions.POST("/:session_id/workbench/files/upload", h.UploadFile)
		sessions.POST("/:session_id/workbench/files/download", h.DownloadFile)
		sessions.POST("/:session_id/workbench/files/rename", h.RenameFile)
		sessions.POST("/:session_id/workbench/files/delete", h.DeleteFile)
		sessions.POST("/:session_id/workbench/artifacts", h.PublishArtifact)
		sessions.GET("/:id/workbench/artifacts/:artifact_id/versions/:version", h.GetArtifact)
		sessions.GET("/:id/workbench/artifacts/:artifact_id/versions/:version/download", h.DownloadArtifact)
		sessions.GET("/:id/workbench/artifacts/:artifact_id/versions/:version/preview", h.PreviewArtifact)
		sessions.POST("/:session_id/workbench/skill-runs/presentation", h.PublishPresentationSkillCandidate)
	}
}
