package handler

import (
	"context"
	"encoding/base64"
	"net/http"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

type workbenchFileManager interface {
	Browse(ctx context.Context, scope service.WorkbenchFileScope, ref types.WorkbenchFileRef) ([]types.WorkbenchFileEntry, error)
	Upload(ctx context.Context, scope service.WorkbenchFileScope, ref types.WorkbenchFileRef, content []byte) (types.WorkbenchFileEntry, error)
	Download(ctx context.Context, scope service.WorkbenchFileScope, ref types.WorkbenchFileRef) ([]byte, types.WorkbenchFileEntry, error)
	Rename(ctx context.Context, scope service.WorkbenchFileScope, source, target types.WorkbenchFileRef) (types.WorkbenchFileEntry, error)
	Delete(ctx context.Context, scope service.WorkbenchFileScope, ref types.WorkbenchFileRef) error
}

type workbenchFileScopeRequest struct {
	WorkbenchID        string `json:"workbench_id"`
	JobID              string `json:"job_id"`
	ExpectedLeaseEpoch int64  `json:"expected_lease_epoch"`
}

type browseWorkbenchFileRequest struct {
	workbenchFileScopeRequest
	Ref types.WorkbenchFileRef `json:"ref"`
}

type uploadWorkbenchFileRequest struct {
	workbenchFileScopeRequest
	Ref        types.WorkbenchFileRef `json:"ref"`
	ContentB64 string                 `json:"content_b64"`
}

type downloadWorkbenchFileRequest struct {
	workbenchFileScopeRequest
	Ref types.WorkbenchFileRef `json:"ref"`
}

type renameWorkbenchFileRequest struct {
	workbenchFileScopeRequest
	Source types.WorkbenchFileRef `json:"source"`
	Target types.WorkbenchFileRef `json:"target"`
}

type deleteWorkbenchFileRequest struct {
	workbenchFileScopeRequest
	Ref types.WorkbenchFileRef `json:"ref"`
}

func (h *WorkbenchHandler) BrowseFiles(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if !h.requireWorkbenchFiles(c) {
		return
	}
	var req browseWorkbenchFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	entries, err := h.files.Browse(ctx, req.scope(tenantID, session.ID), req.Ref)
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	writeWorkbenchData(c, http.StatusOK, gin.H{"entries": entries})
}

func (h *WorkbenchHandler) UploadFile(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if !h.requireWorkbenchFiles(c) {
		return
	}
	var req uploadWorkbenchFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	content, err := base64.StdEncoding.DecodeString(req.ContentB64)
	if err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "invalid file content")
		return
	}
	entry, err := h.files.Upload(ctx, req.scope(tenantID, session.ID), req.Ref, content)
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	writeWorkbenchData(c, http.StatusCreated, gin.H{"entry": entry})
}

func (h *WorkbenchHandler) DownloadFile(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if !h.requireWorkbenchFiles(c) {
		return
	}
	var req downloadWorkbenchFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	content, entry, err := h.files.Download(ctx, req.scope(tenantID, session.ID), req.Ref)
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	writeWorkbenchData(c, http.StatusOK, gin.H{"entry": entry, "content_b64": base64.StdEncoding.EncodeToString(content)})
}

func (h *WorkbenchHandler) RenameFile(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if !h.requireWorkbenchFiles(c) {
		return
	}
	var req renameWorkbenchFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	entry, err := h.files.Rename(ctx, req.scope(tenantID, session.ID), req.Source, req.Target)
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	writeWorkbenchData(c, http.StatusOK, gin.H{"entry": entry})
}

func (h *WorkbenchHandler) DeleteFile(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if !h.requireWorkbenchFiles(c) {
		return
	}
	var req deleteWorkbenchFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	if err := h.files.Delete(ctx, req.scope(tenantID, session.ID), req.Ref); err != nil {
		h.writeMappedError(c, err)
		return
	}
	writeWorkbenchData(c, http.StatusOK, gin.H{"deleted": true})
}

func (r workbenchFileScopeRequest) scope(tenantID uint64, chatSessionID string) service.WorkbenchFileScope {
	return service.WorkbenchFileScope{
		TenantID:           tenantID,
		ChatSessionID:      chatSessionID,
		WorkbenchSessionID: r.WorkbenchID,
		JobID:              r.JobID,
		ExpectedLeaseEpoch: r.ExpectedLeaseEpoch,
	}
}

func (h *WorkbenchHandler) requireWorkbenchFiles(c *gin.Context) bool {
	if h.files == nil {
		writeWorkbenchError(c, http.StatusServiceUnavailable, workbenchErrorRunnerUnavailable, "workbench file service is unavailable")
		return false
	}
	return true
}
