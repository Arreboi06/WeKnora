package handler

import (
	"errors"
	"io"
	"net/http"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type CitationProfileHandler struct {
	service interfaces.CitationProfileService
}

func NewCitationProfileHandler(service interfaces.CitationProfileService) *CitationProfileHandler {
	return &CitationProfileHandler{service: service}
}

// GetStatus returns the caller-scoped citation profile status for one knowledge base.
func (h *CitationProfileHandler) GetStatus(c *gin.Context) {
	citationProfileNoStore(c)
	if h == nil || h.service == nil {
		c.Error(apperrors.NewServiceUnavailableError("citation profile service is unavailable"))
		return
	}

	kbID := strings.TrimSpace(c.Param("kb_id"))
	if kbID == "" {
		c.Error(apperrors.NewBadRequestError("citation_profile_invalid_request"))
		return
	}

	status, err := h.service.GetStatus(c.Request.Context(), kbID)
	if err != nil {
		c.Error(citationProfileAppError(err, "failed to load citation profile status"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": status})
}

func (h *CitationProfileHandler) SetEnrollment(c *gin.Context) {
	citationProfileNoStore(c)
	if h == nil || h.service == nil {
		c.Error(apperrors.NewServiceUnavailableError("citation profile service is unavailable"))
		return
	}
	var req types.CitationProfileEnrollmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError("citation_profile_invalid_request"))
		return
	}
	resp, err := h.service.SetEnrollment(c.Request.Context(), c.Param("kb_id"), req)
	if err != nil {
		c.Error(citationProfileAppError(err, "failed to update citation profile enrollment"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": resp})
}

func (h *CitationProfileHandler) CreateExport(c *gin.Context) {
	citationProfileNoStore(c)
	if h == nil || h.service == nil {
		c.Error(apperrors.NewServiceUnavailableError("citation profile service is unavailable"))
		return
	}
	var req types.CitationProfileExportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError("citation_profile_invalid_request"))
		return
	}
	resp, err := h.service.CreateExport(c.Request.Context(), c.Param("kb_id"), req)
	if err != nil {
		c.Error(citationProfileAppError(err, "failed to create citation profile export"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": resp})
}

func (h *CitationProfileHandler) GetExport(c *gin.Context) {
	citationProfileNoStore(c)
	if h == nil || h.service == nil {
		c.Error(apperrors.NewServiceUnavailableError("citation profile service is unavailable"))
		return
	}
	resp, err := h.service.GetExport(c.Request.Context(), c.Param("kb_id"), c.Param("operation_id"))
	if err != nil {
		c.Error(citationProfileAppError(err, "failed to load citation profile export"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": resp})
}

func (h *CitationProfileHandler) DownloadExport(c *gin.Context) {
	citationProfileNoStore(c)
	if h == nil || h.service == nil {
		c.Error(apperrors.NewServiceUnavailableError("citation profile service is unavailable"))
		return
	}
	payload, err := h.service.DownloadExport(c.Request.Context(), c.Param("kb_id"), c.Param("operation_id"))
	if err != nil {
		c.Error(citationProfileAppError(err, "failed to download citation profile export"))
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

func (h *CitationProfileHandler) DeleteCurrentScope(c *gin.Context) {
	citationProfileNoStore(c)
	if h == nil || h.service == nil {
		c.Error(apperrors.NewServiceUnavailableError("citation profile service is unavailable"))
		return
	}
	var req types.CitationProfileDeleteRequest
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
			c.Error(apperrors.NewBadRequestError("citation_profile_invalid_request"))
			return
		}
	}
	resp, err := h.service.RequestCurrentACLDelete(c.Request.Context(), c.Param("kb_id"), req)
	if err != nil {
		c.Error(citationProfileAppError(err, "failed to request citation profile deletion"))
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": resp})
}

func (h *CitationProfileHandler) DeleteBlindScope(c *gin.Context) {
	citationProfileNoStore(c)
	if h == nil || h.service == nil {
		c.Error(apperrors.NewServiceUnavailableError("citation profile service is unavailable"))
		return
	}
	resp, err := h.service.RequestBlindDelete(c.Request.Context(), c.Param("kb_id"))
	if err != nil {
		c.Error(citationProfileAppError(err, "failed to request citation profile deletion"))
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": resp})
}

func citationProfileNoStore(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Pragma", "no-cache")
}

func citationProfileAppError(err error, fallback string) *apperrors.AppError {
	switch {
	case errors.Is(err, types.ErrCitationProfileInvalidRequest):
		return apperrors.NewBadRequestError("citation_profile_invalid_request")
	case errors.Is(err, types.ErrCitationProfileAuthRequired):
		return apperrors.NewUnauthorizedError("citation_profile_auth_required")
	case errors.Is(err, types.ErrCitationProfileChanged):
		return apperrors.NewConflictError("profile_changed")
	case errors.Is(err, types.ErrCitationProfileIdempotencyConflict):
		return apperrors.NewConflictError("citation_profile_idempotency_conflict")
	case errors.Is(err, types.ErrCitationProfileQuotaExceeded):
		return apperrors.NewTooManyRequestsError("citation_profile_quota_exceeded")
	case errors.Is(err, types.ErrCitationProfileDeleted):
		return &apperrors.AppError{Code: apperrors.ErrConflict, Message: "citation_profile_deleted", HTTPCode: http.StatusGone}
	case errors.Is(err, types.ErrCitationProfileNotFound):
		return apperrors.NewNotFoundError("citation_profile_not_found")
	case errors.Is(err, types.ErrCitationProfileUnavailable):
		return apperrors.NewServiceUnavailableError("citation_profile_temporarily_unavailable")
	case errors.Is(err, types.ErrCitationProfileDisabled):
		return apperrors.NewServiceUnavailableError("citation_profile_disabled")
	default:
		return apperrors.NewInternalServerError(fallback)
	}
}
