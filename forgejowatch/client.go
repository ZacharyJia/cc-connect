package forgejowatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

type ForgejoAPI interface {
	ListAssignedIssues(ctx context.Context, state string) ([]ForgejoIssue, error)
	ListCreatedPulls(ctx context.Context, state string) ([]ForgejoIssue, error)
	ListComments(ctx context.Context, owner, repo string, number int64) ([]ForgejoComment, error)
	GetPull(ctx context.Context, owner, repo string, number int64) (ForgejoIssue, error)
}

type AdminAPI interface {
	CreateSession(ctx context.Context, req CreateSessionRequest) (CreateSessionResponse, error)
	ListSessionGroups(ctx context.Context, project string) ([]AdminSessionGroup, error)
	SubmitPrompt(ctx context.Context, req SubmitPromptRequest) error
}

type ForgejoClient struct {
	baseURL    string
	token      string
	username   string
	httpClient *http.Client
}

type AdminClient struct {
	project    string
	socketPath string
	httpClient *http.Client
}

type ForgejoIssue struct {
	Number     int64             `json:"number"`
	Index      int64             `json:"index"`
	Title      string            `json:"title"`
	Body       string            `json:"body"`
	HTMLURL    string            `json:"html_url"`
	UpdatedAt  time.Time         `json:"updated_at"`
	State      string            `json:"state"`
	Repository ForgejoRepository `json:"repository"`
}

type ForgejoRepository struct {
	FullName string      `json:"full_name"`
	Name     string      `json:"name"`
	Owner    ForgejoUser `json:"owner"`
}

type ForgejoUser struct {
	Login string `json:"login"`
}

type ForgejoComment struct {
	ID        int64       `json:"id"`
	Body      string      `json:"body"`
	HTMLURL   string      `json:"html_url"`
	CreatedAt time.Time   `json:"created_at"`
	User      ForgejoUser `json:"user"`
}

type CreateSessionRequest struct {
	Project    string `json:"project"`
	SessionKey string `json:"session_key"`
	Name       string `json:"name"`
	WorkDir    string `json:"work_dir"`
}

type CreateSessionResponse struct {
	SessionKey string `json:"session_key"`
	Session    struct {
		ID string `json:"id"`
	} `json:"session"`
}

type SubmitPromptRequest struct {
	Project    string `json:"project"`
	SessionKey string `json:"session_key"`
	SessionID  string `json:"session_id"`
	Prompt     string `json:"prompt"`
}

type AdminSessionGroup struct {
	SessionKey string                `json:"session_key"`
	Sessions   []AdminSessionSummary `json:"sessions"`
}

type AdminSessionSummary struct {
	ID   string `json:"id"`
	Busy bool   `json:"busy"`
}

func NewForgejoClient(baseURL, token, username string) *ForgejoClient {
	return &ForgejoClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		token:    token,
		username: username,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func NewAdminClient(project, socketPath string) *AdminClient {
	return &AdminClient{
		project:    project,
		socketPath: socketPath,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
			Timeout: 15 * time.Second,
		},
	}
}

func (c *ForgejoClient) ListAssignedIssues(ctx context.Context, state string) ([]ForgejoIssue, error) {
	return c.searchIssues(ctx, state, "issues", "assigned")
}

func (c *ForgejoClient) ListCreatedPulls(ctx context.Context, state string) ([]ForgejoIssue, error) {
	return c.searchIssues(ctx, state, "pulls", "created")
}

func (c *ForgejoClient) ListComments(ctx context.Context, owner, repo string, number int64) ([]ForgejoComment, error) {
	var all []ForgejoComment
	for page := 1; ; page++ {
		values := url.Values{}
		values.Set("page", strconv.Itoa(page))
		values.Set("limit", "50")

		var batch []ForgejoComment
		if err := c.get(ctx, "/api/v1/repos/"+owner+"/"+repo+"/issues/"+strconv.FormatInt(number, 10)+"/comments", values, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 50 {
			break
		}
	}
	return all, nil
}

func (c *ForgejoClient) GetPull(ctx context.Context, owner, repo string, number int64) (ForgejoIssue, error) {
	var pull ForgejoIssue
	err := c.get(ctx, "/api/v1/repos/"+owner+"/"+repo+"/pulls/"+strconv.FormatInt(number, 10), nil, &pull)
	return pull, err
}

func (c *ForgejoClient) searchIssues(ctx context.Context, state, issueType, mode string) ([]ForgejoIssue, error) {
	var all []ForgejoIssue
	for page := 1; ; page++ {
		values := url.Values{}
		values.Set("type", issueType)
		values.Set("state", state)
		values.Set(mode, "true")
		values.Set("page", strconv.Itoa(page))
		values.Set("limit", "50")

		var batch []ForgejoIssue
		if err := c.get(ctx, "/api/v1/repos/issues/search", values, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 50 {
			break
		}
	}
	return all, nil
}

func (c *ForgejoClient) get(ctx context.Context, p string, values url.Values, out any) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return err
	}
	u.Path = path.Join(u.Path, p)
	if len(values) > 0 {
		u.RawQuery = values.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("forgejo api %s: %s", p, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode forgejo response: %w", err)
	}
	return nil
}

func (i ForgejoIssue) NumberValue() int64 {
	if i.Number != 0 {
		return i.Number
	}
	return i.Index
}

func (c *AdminClient) CreateSession(ctx context.Context, req CreateSessionRequest) (CreateSessionResponse, error) {
	if req.Project == "" {
		req.Project = c.project
	}
	var result CreateSessionResponse
	err := c.post(ctx, "/api/admin/session/create", req, http.StatusCreated, &result)
	return result, err
}

func (c *AdminClient) ListSessionGroups(ctx context.Context, project string) ([]AdminSessionGroup, error) {
	if project == "" {
		project = c.project
	}
	values := url.Values{}
	if project != "" {
		values.Set("project", project)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/api/admin/sessions?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("admin api /api/admin/sessions: %s", strings.TrimSpace(string(body)))
	}

	var payload struct {
		Groups []AdminSessionGroup `json:"groups"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode admin response: %w", err)
	}
	return payload.Groups, nil
}

func (c *AdminClient) SubmitPrompt(ctx context.Context, req SubmitPromptRequest) error {
	if req.Project == "" {
		req.Project = c.project
	}
	return c.post(ctx, "/api/admin/prompt", req, http.StatusAccepted, nil)
}

func (c *AdminClient) post(ctx context.Context, p string, payload any, expectedStatus int, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+p, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != expectedStatus {
		return fmt.Errorf("admin api %s: %s", p, strings.TrimSpace(string(respBody)))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode admin response: %w", err)
		}
	}
	return nil
}
