package handler

import (
	"net/http"
	"strconv"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// CodeWikiHandler exposes the code wiki management API. Reads are Viewer+,
// all management/mutation routes are guarded by OwnedWikiKBOrAdmin +
// KBAccessWrite in the router (same matrix as wiki pages).
type CodeWikiHandler struct {
	service interfaces.CodeWikiService
}

// NewCodeWikiHandler builds the handler.
func NewCodeWikiHandler(service interfaces.CodeWikiService) *CodeWikiHandler {
	return &CodeWikiHandler{service: service}
}

// resolveCodeWikiKB validates the :kb_id route parameter and rewrites the
// context's tenant to the KB owner's (shared access), matching the wiki
// handler conventions.
func (h *CodeWikiHandler) resolveCodeWikiKB(c *gin.Context) (string, bool) {
	kbID := c.Param("kb_id")
	if kbID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "knowledge base id is required"})
		return "", false
	}
	return kbID, true
}

// GetAnalysisProfile handles GET /knowledgebase/:kb_id/code-wiki/analysis-profile.
func (h *CodeWikiHandler) GetAnalysisProfile(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	profile, err := h.service.GetAnalysisProfile(c.Request.Context(), kbID)
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": profile})
}

// UpdateAnalysisProfile handles PUT /knowledgebase/:kb_id/code-wiki/analysis-profile.
func (h *CodeWikiHandler) UpdateAnalysisProfile(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	var req types.CodeWikiAnalysisProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	profile, err := h.service.UpdateAnalysisProfile(c.Request.Context(), kbID, &req)
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": profile})
}

// TestAnalysisProfile handles POST /knowledgebase/:kb_id/code-wiki/analysis-profile/test.
// The layered capability check may take seconds (CLI version probe / MCP
// ListTools); it stays synchronous in the first version and returns the
// full report.
func (h *CodeWikiHandler) TestAnalysisProfile(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	profile, err := h.service.TestAnalysisProfile(c.Request.Context(), kbID)
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": profile})
}

// ListRepositories handles GET /knowledgebase/:kb_id/code-wiki/repositories.
func (h *CodeWikiHandler) ListRepositories(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	repos, err := h.service.ListRepositories(c.Request.Context(), kbID)
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": repos})
}

// CreateRepository handles POST /knowledgebase/:kb_id/code-wiki/repositories.
func (h *CodeWikiHandler) CreateRepository(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	var req types.CodeWikiRepositoryUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	repo, err := h.service.CreateRepository(c.Request.Context(), kbID, &req)
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": repo})
}

// UpdateRepository handles PATCH /knowledgebase/:kb_id/code-wiki/repositories/:id.
func (h *CodeWikiHandler) UpdateRepository(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	var req types.CodeWikiRepositoryUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	repo, err := h.service.UpdateRepository(c.Request.Context(), kbID, c.Param("id"), &req)
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": repo})
}

// DeleteRepository handles DELETE /knowledgebase/:kb_id/code-wiki/repositories/:id.
func (h *CodeWikiHandler) DeleteRepository(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	if err := h.service.DeleteRepository(c.Request.Context(), kbID, c.Param("id")); err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// SyncRepositories handles POST /knowledgebase/:kb_id/code-wiki/sync.
// Returns 202 — the actual snapshot work runs in the background task.
func (h *CodeWikiHandler) SyncRepositories(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	var req types.CodeWikiSyncRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
			return
		}
	}
	count, err := h.service.SyncRepositories(c.Request.Context(), kbID, req.RepositoryIDs)
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": gin.H{"repositories": count}})
}

// ListRuns handles GET /knowledgebase/:kb_id/code-wiki/runs.
func (h *CodeWikiHandler) ListRuns(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	runs, err := h.service.ListRuns(c.Request.Context(), kbID)
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": runs})
}

// CreateRun handles POST /knowledgebase/:kb_id/code-wiki/runs. Returns 202
// with the run id; progress is polled via the run detail endpoint.
func (h *CodeWikiHandler) CreateRun(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	var req types.CodeWikiRunCreateRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
			return
		}
	}
	run, err := h.service.CreateRun(c.Request.Context(), kbID, &req)
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": run})
}

// GetRun handles GET /knowledgebase/:kb_id/code-wiki/runs/:id, including
// the frozen repository manifest and staged pages for staged runs.
func (h *CodeWikiHandler) GetRun(c *gin.Context) {
	ctx := c.Request.Context()
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	run, manifest, pages, err := h.service.GetRunDetail(ctx, kbID, c.Param("id"))
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"run":       run,
		"manifest":  manifest,
		"pages":     pages,
	}})
}

// CancelRun handles POST /knowledgebase/:kb_id/code-wiki/runs/:id/cancel.
func (h *CodeWikiHandler) CancelRun(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	if err := h.service.CancelRun(c.Request.Context(), kbID, c.Param("id")); err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true})
}

// RetryRun handles POST /knowledgebase/:kb_id/code-wiki/runs/:id/retry.
func (h *CodeWikiHandler) RetryRun(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	run, err := h.service.RetryRun(c.Request.Context(), kbID, c.Param("id"))
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": run})
}

// PublishRun handles POST /knowledgebase/:kb_id/code-wiki/runs/:id/publish.
func (h *CodeWikiHandler) PublishRun(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	var req types.CodeWikiRunPublishRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
			return
		}
	}
	run, err := h.service.PublishRun(c.Request.Context(), kbID, c.Param("id"), &req)
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": run})
}

// GetEvidence handles GET /knowledgebase/:kb_id/code-wiki/evidence/:id.
func (h *CodeWikiHandler) GetEvidence(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	view, err := h.service.GetEvidence(c.Request.Context(), kbID, c.Param("id"))
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": view})
}

// ListPageEvidence handles
// GET /knowledgebase/:kb_id/code-wiki/pages/:page_id/evidence?version=N —
// the citations bound to one published page version (wiki browser source
// sidebar). version defaults to the page's current version.
func (h *CodeWikiHandler) ListPageEvidence(c *gin.Context) {
	kbID, ok := h.resolveCodeWikiKB(c)
	if !ok {
		return
	}
	version := 0
	if raw := c.Query("version"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			version = parsed
		}
	}
	views, err := h.service.ListPageEvidence(c.Request.Context(), kbID, c.Param("page_id"), version)
	if err != nil {
		respondCodeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": views})
}

// respondCodeWikiError maps service errors to HTTP responses. Typed
// AppErrors (bad request / not found) render their own status via the
// error middleware; known domain sentinels fall back by message class.
func respondCodeWikiError(c *gin.Context, err error) {
	logger.Error(c.Request.Context(), "code wiki api error", err)
	if appErr, ok := apperrors.IsAppError(err); ok {
		c.Error(appErr)
		return
	}
	status := http.StatusInternalServerError
	switch err {
	case types.ErrCodeWikiRepoNotFound, types.ErrCodeWikiRunNotFound,
		types.ErrCodeWikiSnapshotNotFound, types.ErrCodeWikiEvidenceNotFound,
		types.ErrCodeWikiProfileNotFound, types.ErrCodeWikiArtifactNotFound:
		status = http.StatusNotFound
	case types.ErrCodeWikiNotCodeWikiKB, types.ErrCodeWikiRunNotStaged,
		types.ErrCodeWikiRunActiveExists, types.ErrCodeWikiNoRepositories,
		types.ErrCodeWikiSourceMismatch, types.ErrCodeWikiProfileInvalid,
		types.ErrCodeWikiCapabilitiesMissing, types.ErrCodeWikiPublishConflict:
		status = http.StatusBadRequest
	}
	c.JSON(status, gin.H{"success": false, "message": err.Error()})
}
