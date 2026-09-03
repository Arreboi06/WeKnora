package handler

import (
	"net/http"
	"strconv"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/gin-gonic/gin"
)

func (h *CitationProfileHandler) ListNodes(c *gin.Context) {
	citationProfileNoStore(c)
	if h == nil || h.service == nil {
		c.Error(apperrors.NewServiceUnavailableError("citation profile service is unavailable"))
		return
	}
	pageSize, err := citationProfilePageSizeParam(c.Query("page_size"))
	if err != nil {
		c.Error(apperrors.NewBadRequestError("citation_profile_invalid_request"))
		return
	}
	resp, err := h.service.ListNodes(c.Request.Context(), c.Param("kb_id"), c.Query("cursor"), pageSize)
	if err != nil {
		c.Error(citationProfileAppError(err, "failed to load citation profile nodes"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": resp})
}

func (h *CitationProfileHandler) GetGraph(c *gin.Context) {
	citationProfileNoStore(c)
	if h == nil || h.service == nil {
		c.Error(apperrors.NewServiceUnavailableError("citation profile service is unavailable"))
		return
	}
	resp, err := h.service.GetGraph(c.Request.Context(), c.Param("kb_id"))
	if err != nil {
		c.Error(citationProfileAppError(err, "failed to load citation profile graph"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": resp})
}

func (h *CitationProfileHandler) ListNodeEvidence(c *gin.Context) {
	citationProfileNoStore(c)
	if h == nil || h.service == nil {
		c.Error(apperrors.NewServiceUnavailableError("citation profile service is unavailable"))
		return
	}
	pageSize, err := citationProfilePageSizeParam(c.Query("page_size"))
	if err != nil {
		c.Error(apperrors.NewBadRequestError("citation_profile_invalid_request"))
		return
	}
	resp, err := h.service.ListNodeEvidence(c.Request.Context(), c.Param("kb_id"), c.Param("page_uuid"), c.Query("cursor"), pageSize)
	if err != nil {
		c.Error(citationProfileAppError(err, "failed to load citation profile evidence"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": resp})
}

func citationProfilePageSizeParam(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	pageSize, err := strconv.Atoi(raw)
	if err != nil || pageSize <= 0 {
		return 0, strconv.ErrSyntax
	}
	return pageSize, nil
}
