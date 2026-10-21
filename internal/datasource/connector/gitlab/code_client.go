package gitlab

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource"
)

// CodeClient is the code-wiki facade over the GitLab API: commit resolution
// and repository archive download for building immutable snapshots. It
// reuses the same base-URL validation and SSRF-controlled HTTP client as
// the document connector, and never runs repository code — archives are
// only downloaded and unpacked by the caller.
type CodeClient struct {
	client *client
	// Project is the resolved project info for one binding.
	Project *ProjectInfo
}

// ProjectInfo is the subset of GitLab project metadata the code wiki needs.
type ProjectInfo struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	Name              string `json:"name"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
}

// NewCodeClient builds a client from the data source's decrypted
// credentials (base_url + access_token).
func NewCodeClient(baseURL, token string) (*CodeClient, error) {
	c, err := newClient(baseURL, token)
	if err != nil {
		return nil, err
	}
	return &CodeClient{client: c}, nil
}

// GetProject resolves a project by numeric id or group/project path.
func (c *CodeClient) GetProject(ctx context.Context, projectID string) (*ProjectInfo, error) {
	var p project
	if err := c.client.get(ctx, "/projects/"+projectPath(projectID), &p); err != nil {
		return nil, fmt.Errorf("gitlab project %s: %w", projectID, err)
	}
	return &ProjectInfo{
		ID:                p.ID,
		PathWithNamespace: p.PathWithNamespace,
		Name:              p.Name,
		WebURL:            p.WebURL,
		DefaultBranch:     p.DefaultBranch,
	}, nil
}

// ResolveCommitSHA resolves a ref (branch/tag/sha) to a commit SHA. The
// snapshot flow always downloads content by the RESOLVED SHA — never the
// moving ref — so a branch advancing mid-sync cannot produce a mixed
// snapshot (plan §2 correction).
func (c *CodeClient) ResolveCommitSHA(ctx context.Context, projectID, ref string) (string, error) {
	sha, err := c.client.commitSHA(ctx, projectID, ref)
	if err != nil {
		return "", fmt.Errorf("gitlab resolve %s@%s: %w", projectID, ref, err)
	}
	if strings.TrimSpace(sha) == "" {
		return "", fmt.Errorf("gitlab resolve %s@%s: empty commit sha", projectID, ref)
	}
	return sha, nil
}

// ArchiveFormat is the archive flavor downloaded for snapshots. tar.gz is
// the smallest GitLab-supported format with streaming unpack support.
const ArchiveFormat = "tar.gz"

// DownloadArchive streams the repository archive at a FIXED commit SHA.
// The caller must bound total bytes read (io.LimitedReader) and unpack with
// path-traversal checks; this method only handles transport.
func (c *CodeClient) DownloadArchive(ctx context.Context, projectID, commitSHA string) (io.ReadCloser, int64, error) {
	endpoint := fmt.Sprintf(
		"/projects/%s/repository/archive.%s?sha=%s",
		projectPath(projectID), ArchiveFormat, url.QueryEscape(commitSHA),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.client.baseURL+endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.client.token)
	resp, err := c.client.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("gitlab archive %s@%s: %w", projectID, commitSHA, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, 0, &apiError{endpoint: endpoint, status: resp.StatusCode}
	}
	// Content-Length is advisory for gzip streams; -1 = unknown.
	var size int64 = -1
	if resp.ContentLength > 0 {
		size = resp.ContentLength
	}
	return resp.Body, size, nil
}

// ensure the connector package keeps exposing its shared helpers.
var _ = datasource.NewConnectorHTTPClient
