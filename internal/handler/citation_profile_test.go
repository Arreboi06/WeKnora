package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type stubCitationProfileService struct {
	interfaces.CitationProfileService

	getStatus          func(ctx context.Context, kbID string) (*types.CitationProfileStatus, error)
	listNodes          func(ctx context.Context, kbID, cursor string, pageSize int) (*types.CitationProfileNodeListResponse, error)
	getGraph           func(ctx context.Context, kbID string) (*types.CitationProfileGraphResponse, error)
	listNodeEvidence   func(ctx context.Context, kbID, pageUUID, cursor string, pageSize int) (*types.CitationProfileNodeEvidenceResponse, error)
	applyCorrection    func(ctx context.Context, kbID string, req types.CitationProfileCorrectionRequest) (*types.CitationProfileCorrectionResponse, error)
	setEnrollment      func(ctx context.Context, kbID string, req types.CitationProfileEnrollmentRequest) (*types.CitationProfileEnrollmentResponse, error)
	createExport       func(ctx context.Context, kbID string, req types.CitationProfileExportRequest) (*types.CitationProfileExportOperationResponse, error)
	getExport          func(ctx context.Context, kbID string, operationID string) (*types.CitationProfileExportOperationResponse, error)
	downloadExport     func(ctx context.Context, kbID string, operationID string) ([]byte, error)
	requestCurrentACL  func(ctx context.Context, kbID string, req types.CitationProfileDeleteRequest) (*types.CitationProfileDeleteResponse, error)
	requestBlindDelete func(ctx context.Context, kbID string) (*types.CitationProfileDeleteResponse, error)
}

func (s *stubCitationProfileService) GetStatus(ctx context.Context, kbID string) (*types.CitationProfileStatus, error) {
	return s.getStatus(ctx, kbID)
}

func (s *stubCitationProfileService) ListNodes(ctx context.Context, kbID, cursor string, pageSize int) (*types.CitationProfileNodeListResponse, error) {
	return s.listNodes(ctx, kbID, cursor, pageSize)
}

func (s *stubCitationProfileService) GetGraph(ctx context.Context, kbID string) (*types.CitationProfileGraphResponse, error) {
	return s.getGraph(ctx, kbID)
}

func (s *stubCitationProfileService) ListNodeEvidence(ctx context.Context, kbID, pageUUID, cursor string, pageSize int) (*types.CitationProfileNodeEvidenceResponse, error) {
	return s.listNodeEvidence(ctx, kbID, pageUUID, cursor, pageSize)
}

func (s *stubCitationProfileService) ApplyCorrection(ctx context.Context, kbID string, req types.CitationProfileCorrectionRequest) (*types.CitationProfileCorrectionResponse, error) {
	return s.applyCorrection(ctx, kbID, req)
}

func (s *stubCitationProfileService) SetEnrollment(ctx context.Context, kbID string, req types.CitationProfileEnrollmentRequest) (*types.CitationProfileEnrollmentResponse, error) {
	return s.setEnrollment(ctx, kbID, req)
}

func (s *stubCitationProfileService) CreateExport(ctx context.Context, kbID string, req types.CitationProfileExportRequest) (*types.CitationProfileExportOperationResponse, error) {
	return s.createExport(ctx, kbID, req)
}

func (s *stubCitationProfileService) GetExport(ctx context.Context, kbID string, operationID string) (*types.CitationProfileExportOperationResponse, error) {
	return s.getExport(ctx, kbID, operationID)
}

func (s *stubCitationProfileService) DownloadExport(ctx context.Context, kbID string, operationID string) ([]byte, error) {
	return s.downloadExport(ctx, kbID, operationID)
}

func (s *stubCitationProfileService) RequestCurrentACLDelete(ctx context.Context, kbID string, req types.CitationProfileDeleteRequest) (*types.CitationProfileDeleteResponse, error) {
	return s.requestCurrentACL(ctx, kbID, req)
}

func (s *stubCitationProfileService) RequestBlindDelete(ctx context.Context, kbID string) (*types.CitationProfileDeleteResponse, error) {
	return s.requestBlindDelete(ctx, kbID)
}

func newCitationProfileTestRouter(h *CitationProfileHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.GET("/kb/:kb_id/status", h.GetStatus)
	r.GET("/kb/:kb_id/nodes", h.ListNodes)
	r.GET("/kb/:kb_id/graph", h.GetGraph)
	r.GET("/kb/:kb_id/nodes/:page_uuid/evidence", h.ListNodeEvidence)
	r.PUT("/kb/:kb_id/enrollment", h.SetEnrollment)
	r.POST("/kb/:kb_id/corrections", h.ApplyCorrection)
	r.POST("/kb/:kb_id/exports", h.CreateExport)
	r.GET("/kb/:kb_id/exports/:operation_id", h.GetExport)
	r.GET("/kb/:kb_id/exports/:operation_id/download", h.DownloadExport)
	r.DELETE("/kb/:kb_id", h.DeleteCurrentScope)
	r.DELETE("/scopes/:kb_id", h.DeleteBlindScope)
	return r
}

func citationProfileRequest(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func requireCitationProfileNoStore(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", got)
	}
	if got := w.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", got)
	}
}

func requireCitationProfileSuccess(t *testing.T, w *httptest.ResponseRecorder, wantStatus int) map[string]any {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("status = %d, want %d body=%s", w.Code, wantStatus, w.Body.String())
	}
	var resp struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("success = false body=%s", w.Body.String())
	}
	return resp.Data
}

func TestCitationProfileHandlerRejectsNonPositivePageSize(t *testing.T) {
	svc := &stubCitationProfileService{
		listNodes: func(context.Context, string, string, int) (*types.CitationProfileNodeListResponse, error) {
			t.Fatal("ListNodes should not be called for invalid page_size")
			return nil, nil
		},
		listNodeEvidence: func(context.Context, string, string, string, int) (*types.CitationProfileNodeEvidenceResponse, error) {
			t.Fatal("ListNodeEvidence should not be called for invalid page_size")
			return nil, nil
		},
	}
	r := newCitationProfileTestRouter(NewCitationProfileHandler(svc))

	for _, path := range []string{"/kb/kb-1/nodes?page_size=0", "/kb/kb-1/nodes/page-1/evidence?page_size=-1"} {
		w := citationProfileRequest(t, r, http.MethodGet, path, nil)
		requireCitationProfileNoStore(t, w)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want %d body=%s", path, w.Code, http.StatusBadRequest, w.Body.String())
		}
	}
}

func TestCitationProfileHandlerMapsDeletedToGone(t *testing.T) {
	svc := &stubCitationProfileService{
		getStatus: func(context.Context, string) (*types.CitationProfileStatus, error) {
			return nil, types.ErrCitationProfileDeleted
		},
	}
	r := newCitationProfileTestRouter(NewCitationProfileHandler(svc))

	w := citationProfileRequest(t, r, http.MethodGet, "/kb/kb-1/status", nil)
	requireCitationProfileNoStore(t, w)
	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want %d body=%s", w.Code, http.StatusGone, w.Body.String())
	}
}
func TestCitationProfileHandlerReadSurfacesForwardParamsAndNoStore(t *testing.T) {
	var nodesArgs struct {
		kbID     string
		cursor   string
		pageSize int
	}
	var evidenceArgs struct {
		kbID     string
		pageUUID string
		cursor   string
		pageSize int
	}
	var graphKB string
	svc := &stubCitationProfileService{
		getStatus: func(_ context.Context, kbID string) (*types.CitationProfileStatus, error) {
			if kbID != "kb-1" {
				t.Fatalf("status kbID = %q", kbID)
			}
			return &types.CitationProfileStatus{
				Enabled:  true,
				Enrolled: true,
				Snapshot: &types.CitationProfileSnapshot{ReadVersion: "7"},
				Guidance: types.CitationProfileDefaultGuidance(),
				Limits:   types.CitationProfileDefaultLimits(),
			}, nil
		},
		listNodes: func(_ context.Context, kbID, cursor string, pageSize int) (*types.CitationProfileNodeListResponse, error) {
			nodesArgs.kbID = kbID
			nodesArgs.cursor = cursor
			nodesArgs.pageSize = pageSize
			return &types.CitationProfileNodeListResponse{
				Snapshot: &types.CitationProfileSnapshot{ReadVersion: "7"},
				Items: []types.CitationProfileNodeDTO{{
					PageUUID:     "page-1",
					PageVersion:  "3",
					Title:        "Page One",
					Overlay:      "known",
					EvidenceHref: "/kb/kb-1/nodes/page-1/evidence",
				}},
				PageSize:     pageSize,
				CompleteList: true,
				Guidance:     types.CitationProfileDefaultGuidance(),
			}, nil
		},
		getGraph: func(_ context.Context, kbID string) (*types.CitationProfileGraphResponse, error) {
			graphKB = kbID
			return &types.CitationProfileGraphResponse{
				Snapshot:        &types.CitationProfileSnapshot{ReadVersion: "7"},
				Nodes:           []types.CitationProfileNodeDTO{{PageUUID: "page-1", Overlay: "known"}},
				Edges:           []types.CitationProfileGraphEdgeDTO{},
				Caps:            types.CitationProfileGraphCaps{MaxNodes: 500, MaxEdges: 2000},
				CompleteListURL: "/kb/kb-1/nodes",
				Guidance:        types.CitationProfileDefaultGuidance(),
			}, nil
		},
		listNodeEvidence: func(_ context.Context, kbID, pageUUID, cursor string, pageSize int) (*types.CitationProfileNodeEvidenceResponse, error) {
			evidenceArgs.kbID = kbID
			evidenceArgs.pageUUID = pageUUID
			evidenceArgs.cursor = cursor
			evidenceArgs.pageSize = pageSize
			return &types.CitationProfileNodeEvidenceResponse{
				Snapshot: &types.CitationProfileSnapshot{ReadVersion: "7"},
				Page:     types.CitationProfileEvidencePageDTO{PageUUID: pageUUID, Title: "Page One"},
				Items:    []types.CitationProfileEvidenceItemDTO{{EventID: "event-1", PageUUID: pageUUID}},
				Guidance: types.CitationProfileDefaultGuidance(),
			}, nil
		},
	}
	r := newCitationProfileTestRouter(NewCitationProfileHandler(svc))

	status := citationProfileRequest(t, r, http.MethodGet, "/kb/kb-1/status", nil)
	requireCitationProfileNoStore(t, status)
	requireCitationProfileSuccess(t, status, http.StatusOK)

	nodes := citationProfileRequest(t, r, http.MethodGet, "/kb/kb-1/nodes?cursor=abc&page_size=42", nil)
	requireCitationProfileNoStore(t, nodes)
	requireCitationProfileSuccess(t, nodes, http.StatusOK)
	if nodesArgs.kbID != "kb-1" || nodesArgs.cursor != "abc" || nodesArgs.pageSize != 42 {
		t.Fatalf("nodes args = %+v", nodesArgs)
	}

	graph := citationProfileRequest(t, r, http.MethodGet, "/kb/kb-1/graph", nil)
	requireCitationProfileNoStore(t, graph)
	requireCitationProfileSuccess(t, graph, http.StatusOK)
	if graphKB != "kb-1" {
		t.Fatalf("graph kbID = %q", graphKB)
	}

	evidence := citationProfileRequest(t, r, http.MethodGet, "/kb/kb-1/nodes/page-1/evidence?cursor=def&page_size=12", nil)
	requireCitationProfileNoStore(t, evidence)
	requireCitationProfileSuccess(t, evidence, http.StatusOK)
	if evidenceArgs.kbID != "kb-1" || evidenceArgs.pageUUID != "page-1" || evidenceArgs.cursor != "def" || evidenceArgs.pageSize != 12 {
		t.Fatalf("evidence args = %+v", evidenceArgs)
	}
}

func TestCitationProfileHandlerMutationsForwardBodiesAndNoStore(t *testing.T) {
	var enrollmentReq types.CitationProfileEnrollmentRequest
	var correctionReq types.CitationProfileCorrectionRequest
	var exportReq types.CitationProfileExportRequest
	var deleteReq types.CitationProfileDeleteRequest
	var blindKB string
	svc := &stubCitationProfileService{
		setEnrollment: func(_ context.Context, kbID string, req types.CitationProfileEnrollmentRequest) (*types.CitationProfileEnrollmentResponse, error) {
			if kbID != "kb-1" {
				t.Fatalf("enrollment kbID = %q", kbID)
			}
			enrollmentReq = req
			return &types.CitationProfileEnrollmentResponse{
				Enabled:  req.Enabled,
				Enrolled: req.Enabled,
				Snapshot: &types.CitationProfileSnapshot{ReadVersion: "8"},
			}, nil
		},
		applyCorrection: func(_ context.Context, kbID string, req types.CitationProfileCorrectionRequest) (*types.CitationProfileCorrectionResponse, error) {
			if kbID != "kb-1" {
				t.Fatalf("correction kbID = %q", kbID)
			}
			correctionReq = req
			return &types.CitationProfileCorrectionResponse{
				CorrectionID: "corr-1",
				Action:       req.Action,
				Snapshot:     &types.CitationProfileSnapshot{ReadVersion: "9"},
				Result:       types.CitationProfileCorrectionStateRejected,
			}, nil
		},
		createExport: func(_ context.Context, kbID string, req types.CitationProfileExportRequest) (*types.CitationProfileExportOperationResponse, error) {
			if kbID != "kb-1" {
				t.Fatalf("export kbID = %q", kbID)
			}
			exportReq = req
			return &types.CitationProfileExportOperationResponse{
				OperationID:   "op-export",
				Status:        types.CitationProfileOperationStatusPreparing,
				Snapshot:      &types.CitationProfileSnapshot{ReadVersion: "9"},
				DownloadURL:   nil,
				SchemaVersion: types.CitationProfileExportSchemaVersion,
			}, nil
		},
		requestCurrentACL: func(_ context.Context, kbID string, req types.CitationProfileDeleteRequest) (*types.CitationProfileDeleteResponse, error) {
			if kbID != "kb-1" {
				t.Fatalf("delete kbID = %q", kbID)
			}
			deleteReq = req
			return &types.CitationProfileDeleteResponse{
				OperationID: "op-delete",
				Status:      types.CitationProfileOperationStatusAccepted,
				ReceiptCode: types.CitationProfileReceiptHiddenPurgeScheduled,
			}, nil
		},
		requestBlindDelete: func(_ context.Context, kbID string) (*types.CitationProfileDeleteResponse, error) {
			blindKB = kbID
			return &types.CitationProfileDeleteResponse{
				OperationID: "op-blind",
				Status:      types.CitationProfileOperationStatusAccepted,
				ReceiptCode: types.CitationProfileReceiptAccepted,
			}, nil
		},
	}
	r := newCitationProfileTestRouter(NewCitationProfileHandler(svc))

	enrollment := citationProfileRequest(t, r, http.MethodPut, "/kb/kb-1/enrollment", map[string]any{
		"enabled":               true,
		"expected_read_version": "7",
		"idempotency_key":       "idem-enroll",
	})
	requireCitationProfileNoStore(t, enrollment)
	requireCitationProfileSuccess(t, enrollment, http.StatusOK)
	if !enrollmentReq.Enabled || enrollmentReq.ExpectedReadVersion != "7" || enrollmentReq.IdempotencyKey != "idem-enroll" {
		t.Fatalf("enrollment request = %+v", enrollmentReq)
	}

	correction := citationProfileRequest(t, r, http.MethodPost, "/kb/kb-1/corrections", map[string]any{
		"expected_read_version": "8",
		"idempotency_key":       "idem-correction",
		"action":                types.CitationCorrectionRejectMapping,
		"event_id":              "event-1",
		"page_uuid":             "page-1",
		"reason_code":           "wrong_page",
	})
	requireCitationProfileNoStore(t, correction)
	requireCitationProfileSuccess(t, correction, http.StatusOK)
	if correctionReq.Action != types.CitationCorrectionRejectMapping || correctionReq.EventID != "event-1" || correctionReq.PageUUID != "page-1" {
		t.Fatalf("correction request = %+v", correctionReq)
	}

	export := citationProfileRequest(t, r, http.MethodPost, "/kb/kb-1/exports", map[string]any{
		"expected_read_version": "9",
		"idempotency_key":       "idem-export",
		"format":                types.CitationProfileExportFormatJSON,
	})
	requireCitationProfileNoStore(t, export)
	requireCitationProfileSuccess(t, export, http.StatusOK)
	if exportReq.ExpectedReadVersion != "9" || exportReq.IdempotencyKey != "idem-export" || exportReq.Format != types.CitationProfileExportFormatJSON {
		t.Fatalf("export request = %+v", exportReq)
	}

	deleted := citationProfileRequest(t, r, http.MethodDelete, "/kb/kb-1", map[string]any{
		"expected_read_version": "9",
		"idempotency_key":       "idem-delete",
	})
	requireCitationProfileNoStore(t, deleted)
	requireCitationProfileSuccess(t, deleted, http.StatusAccepted)
	if deleteReq.ExpectedReadVersion != "9" || deleteReq.IdempotencyKey != "idem-delete" {
		t.Fatalf("delete request = %+v", deleteReq)
	}

	blind := citationProfileRequest(t, r, http.MethodDelete, "/scopes/kb-1", nil)
	requireCitationProfileNoStore(t, blind)
	requireCitationProfileSuccess(t, blind, http.StatusAccepted)
	if blindKB != "kb-1" {
		t.Fatalf("blind kbID = %q", blindKB)
	}
}

func TestCitationProfileHandlerDownloadUsesNoStoreAndJSONContentType(t *testing.T) {
	var gotKB string
	var gotOperationID string
	svc := &stubCitationProfileService{
		downloadExport: func(_ context.Context, kbID string, operationID string) ([]byte, error) {
			gotKB = kbID
			gotOperationID = operationID
			return []byte(`{"schema_version":"citation_profile_export_v1"}`), nil
		},
	}
	r := newCitationProfileTestRouter(NewCitationProfileHandler(svc))

	w := citationProfileRequest(t, r, http.MethodGet, "/kb/kb-1/exports/op-1/download", nil)
	requireCitationProfileNoStore(t, w)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want application/json; charset=utf-8", got)
	}
	if gotKB != "kb-1" || gotOperationID != "op-1" {
		t.Fatalf("download args kb=%q op=%q", gotKB, gotOperationID)
	}
}
