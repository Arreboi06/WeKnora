package handler

import (
	"net/http"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func (h *CitationProfileHandler) ApplyCorrection(c *gin.Context) {
	citationProfileNoStore(c)
	if h == nil || h.service == nil {
		c.Error(apperrors.NewServiceUnavailableError("citation profile service is unavailable"))
		return
	}
	var req types.CitationProfileCorrectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError("citation_profile_invalid_request"))
		return
	}
	resp, err := h.service.ApplyCorrection(c.Request.Context(), c.Param("kb_id"), req)
	if err != nil {
		c.Error(citationProfileAppError(err, "failed to apply citation profile correction"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": resp})
}
