package forgejowatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	defaultPollInterval = time.Minute
	defaultState        = "open"
	defaultWorkDir      = "default"
	projectName         = "default"
)

type Config struct {
	Name         string
	BaseURL      string
	Token        string
	Username     string
	SessionKey   string
	PollInterval time.Duration
	WorkDir      string
	State        string
}

type Runner struct {
	cfg     Config
	forgejo ForgejoAPI
	admin   AdminAPI
	store   *StateStore
	now     func() time.Time
}

type StateStore struct {
	path  string
	state *State
}

type State struct {
	LastPollAt     time.Time                  `json:"last_poll_at,omitempty"`
	NextClusterSeq int                        `json:"next_cluster_seq,omitempty"`
	Entities       map[string]*TrackedEntity  `json:"entities"`
	Clusters       map[string]*TrackedCluster `json:"clusters"`
	Aliases        map[string]string          `json:"aliases,omitempty"`
}

type TrackedEntity struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Owner         string    `json:"owner"`
	Repo          string    `json:"repo"`
	RepoFullName  string    `json:"repo_full_name"`
	Number        int64     `json:"number"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
	URL           string    `json:"url"`
	UpdatedAt     time.Time `json:"updated_at"`
	Open          bool      `json:"open"`
	ClusterID     string    `json:"cluster_id"`
	SessionID     string    `json:"session_id"`
	LastCommentID int64     `json:"last_comment_id,omitempty"`
	References    []string  `json:"references,omitempty"`
}

type TrackedCluster struct {
	ID              string         `json:"id"`
	SessionID       string         `json:"session_id"`
	CreatedAt       time.Time      `json:"created_at"`
	LastSubmittedAt time.Time      `json:"last_submitted_at,omitempty"`
	Members         []string       `json:"members"`
	Pending         []PendingEvent `json:"pending,omitempty"`
}

type PendingEvent struct {
	Kind       string           `json:"kind"`
	EntityID   string           `json:"entity_id"`
	OccurredAt time.Time        `json:"occurred_at"`
	Entity     PendingEntity    `json:"entity"`
	Comments   []PendingComment `json:"comments,omitempty"`
}

type PendingEntity struct {
	Kind         string `json:"kind"`
	RepoFullName string `json:"repo_full_name"`
	Number       int64  `json:"number"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	URL          string `json:"url"`
}

type PendingComment struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

type Summary struct {
	Name           string
	LastPollAt     time.Time
	TrackedCount   int
	ClusterCount   int
	PendingCount   int
	PendingCluster int
}

func NewRunner(cfg Config, statePath, socketPath string) (*Runner, error) {
	store, err := LoadStateStore(statePath)
	if err != nil {
		return nil, err
	}
	return NewRunnerWithClients(cfg, store, NewForgejoClient(cfg.BaseURL, cfg.Token, cfg.Username), NewAdminClient(projectName, socketPath)), nil
}

func NewRunnerWithClients(cfg Config, store *StateStore, forgejo ForgejoAPI, admin AdminAPI) *Runner {
	return &Runner{
		cfg:     withDefaults(cfg),
		forgejo: forgejo,
		admin:   admin,
		store:   store,
		now:     time.Now,
	}
}

func withDefaults(cfg Config) Config {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if strings.TrimSpace(cfg.WorkDir) == "" {
		cfg.WorkDir = defaultWorkDir
	}
	if strings.TrimSpace(cfg.State) == "" {
		cfg.State = defaultState
	}
	return cfg
}

func LoadStateStore(path string) (*StateStore, error) {
	state := &State{
		Entities: make(map[string]*TrackedEntity),
		Clusters: make(map[string]*TrackedCluster),
		Aliases:  make(map[string]string),
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, state); err != nil {
			return nil, fmt.Errorf("parse watcher state %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read watcher state %s: %w", path, err)
	}
	if state.Entities == nil {
		state.Entities = make(map[string]*TrackedEntity)
	}
	if state.Clusters == nil {
		state.Clusters = make(map[string]*TrackedCluster)
	}
	if state.Aliases == nil {
		state.Aliases = make(map[string]string)
	}
	return &StateStore{path: path, state: state}, nil
}

func LoadSummary(path, name string) (Summary, error) {
	store, err := LoadStateStore(path)
	if err != nil {
		return Summary{}, err
	}
	pendingCount := 0
	pendingClusters := 0
	for _, cluster := range store.state.Clusters {
		if len(cluster.Pending) == 0 {
			continue
		}
		pendingClusters++
		pendingCount += len(cluster.Pending)
	}
	return Summary{
		Name:           name,
		LastPollAt:     store.state.LastPollAt,
		TrackedCount:   len(store.state.Entities),
		ClusterCount:   len(store.state.Clusters),
		PendingCount:   pendingCount,
		PendingCluster: pendingClusters,
	}, nil
}

func (s *StateStore) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

func (s *StateStore) State() *State {
	return s.state
}

func (r *Runner) Run(ctx context.Context) error {
	if err := r.Sync(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.Sync(ctx); err != nil {
				slog.Error("forgejo-watch sync failed", "watcher", r.cfg.Name, "error", err)
			}
		}
	}
}

func (r *Runner) Sync(ctx context.Context) error {
	state := r.store.State()
	seen := make(map[string]struct{})

	issues, err := r.forgejo.ListAssignedIssues(ctx, r.cfg.State)
	if err != nil {
		return err
	}
	for _, issue := range issues {
		id, err := r.refreshEntity(ctx, state, issue, "issue")
		if err != nil {
			return err
		}
		seen[id] = struct{}{}
	}

	pulls, err := r.forgejo.ListCreatedPulls(ctx, r.cfg.State)
	if err != nil {
		return err
	}
	for _, pull := range pulls {
		id, err := r.refreshEntity(ctx, state, pull, "pull")
		if err != nil {
			return err
		}
		seen[id] = struct{}{}
	}

	for id, entity := range state.Entities {
		if _, ok := seen[id]; ok {
			entity.Open = true
			continue
		}
		entity.Open = false
	}

	state.LastPollAt = r.now()
	if err := r.dispatchNextPending(ctx, state); err != nil {
		return err
	}
	return r.store.Save()
}

func (r *Runner) refreshEntity(ctx context.Context, state *State, item ForgejoIssue, kind string) (string, error) {
	owner := item.Repository.Owner.Login
	repo := item.Repository.Name
	number := item.NumberValue()
	entityID := entityID(owner, repo, number)

	entity, exists := state.Entities[entityID]
	if !exists {
		entity = &TrackedEntity{
			ID:           entityID,
			Kind:         kind,
			Owner:        owner,
			Repo:         repo,
			RepoFullName: item.Repository.FullName,
			Number:       number,
			Open:         true,
		}
		state.Entities[entityID] = entity
	}

	details := item
	if kind == "pull" {
		pull, err := r.forgejo.GetPull(ctx, owner, repo, number)
		if err != nil {
			return "", err
		}
		if pull.Repository.FullName == "" {
			pull.Repository = item.Repository
		}
		details = pull
	}

	entity.Kind = kind
	entity.Owner = owner
	entity.Repo = repo
	entity.RepoFullName = details.Repository.FullName
	entity.Number = number
	entity.Title = details.Title
	entity.Body = details.Body
	entity.URL = details.HTMLURL
	entity.UpdatedAt = details.UpdatedAt
	entity.Open = true
	if kind == "pull" {
		entity.References = parseLinkedReferences(details.Title+"\n"+details.Body, owner, repo)
	} else {
		entity.References = nil
	}

	cluster, err := r.ensureCluster(ctx, state, entity, !exists)
	if err != nil {
		return "", err
	}
	entity.ClusterID = cluster.ID
	entity.SessionID = cluster.SessionID

	comments, err := r.forgejo.ListComments(ctx, owner, repo, number)
	if err != nil {
		return "", err
	}
	slices.SortFunc(comments, func(a, b ForgejoComment) int {
		if cmp := a.CreatedAt.Compare(b.CreatedAt); cmp != 0 {
			return cmp
		}
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		default:
			return 0
		}
	})

	if !exists {
		cluster.Pending = append(cluster.Pending, PendingEvent{
			Kind:       "init",
			EntityID:   entityID,
			OccurredAt: r.now(),
			Entity:     snapshotEntity(entity),
			Comments:   snapshotComments(comments),
		})
	}

	var maxCommentID int64
	var newComments []ForgejoComment
	for _, comment := range comments {
		if comment.ID > maxCommentID {
			maxCommentID = comment.ID
		}
		if comment.ID > entity.LastCommentID {
			newComments = append(newComments, comment)
		}
	}
	if exists && len(newComments) > 0 {
		cluster.Pending = append(cluster.Pending, PendingEvent{
			Kind:       "comment",
			EntityID:   entityID,
			OccurredAt: r.now(),
			Entity:     snapshotEntity(entity),
			Comments:   snapshotComments(newComments),
		})
	}
	entity.LastCommentID = maxCommentID

	return entityID, nil
}

func (r *Runner) ensureCluster(ctx context.Context, state *State, entity *TrackedEntity, isNew bool) (*TrackedCluster, error) {
	candidates := r.relatedClusters(state, entity)
	if entity.ClusterID != "" {
		candidates = append(candidates, entity.ClusterID)
	}
	candidates = canonicalUnique(state, candidates)
	switch len(candidates) {
	case 0:
		if !isNew && entity.ClusterID != "" {
			if cluster, ok := state.Clusters[r.canonicalClusterID(state, entity.ClusterID)]; ok {
				return cluster, nil
			}
		}
		return r.createCluster(ctx, state, entity)
	case 1:
		cluster := state.Clusters[candidates[0]]
		if cluster == nil {
			return r.createCluster(ctx, state, entity)
		}
		ensureMember(cluster, entity.ID)
		return cluster, nil
	default:
		cluster := r.mergeClusters(state, candidates)
		ensureMember(cluster, entity.ID)
		return cluster, nil
	}
}

func (r *Runner) relatedClusters(state *State, entity *TrackedEntity) []string {
	var out []string
	switch entity.Kind {
	case "pull":
		for _, ref := range entity.References {
			if linked, ok := state.Entities[ref]; ok {
				out = append(out, linked.ClusterID)
			}
		}
	case "issue":
		for _, other := range state.Entities {
			if other.Kind != "pull" {
				continue
			}
			for _, ref := range other.References {
				if ref == entity.ID {
					out = append(out, other.ClusterID)
					break
				}
			}
		}
	}
	return out
}

func (r *Runner) createCluster(ctx context.Context, state *State, entity *TrackedEntity) (*TrackedCluster, error) {
	resp, err := r.admin.CreateSession(ctx, CreateSessionRequest{
		Project:    projectName,
		SessionKey: r.cfg.SessionKey,
		Name:       sessionName(entity),
		WorkDir:    r.cfg.WorkDir,
	})
	if err != nil {
		return nil, err
	}

	state.NextClusterSeq++
	clusterID := fmt.Sprintf("cluster-%d", state.NextClusterSeq)
	cluster := &TrackedCluster{
		ID:        clusterID,
		SessionID: resp.Session.ID,
		CreatedAt: r.now(),
		Members:   []string{entity.ID},
	}
	state.Clusters[clusterID] = cluster
	return cluster, nil
}

func (r *Runner) mergeClusters(state *State, ids []string) *TrackedCluster {
	ids = canonicalUnique(state, ids)
	slices.SortFunc(ids, func(a, b string) int {
		left := state.Clusters[a]
		right := state.Clusters[b]
		if left == nil || right == nil {
			return strings.Compare(a, b)
		}
		if cmp := left.CreatedAt.Compare(right.CreatedAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a, b)
	})

	canonical := state.Clusters[ids[0]]
	for _, id := range ids[1:] {
		other := state.Clusters[id]
		if other == nil {
			continue
		}
		for _, memberID := range other.Members {
			ensureMember(canonical, memberID)
			if entity := state.Entities[memberID]; entity != nil {
				entity.ClusterID = canonical.ID
				entity.SessionID = canonical.SessionID
			}
		}
		canonical.Pending = append(canonical.Pending, other.Pending...)
		state.Aliases[id] = canonical.ID
		delete(state.Clusters, id)
	}
	slices.SortFunc(canonical.Pending, func(a, b PendingEvent) int {
		if cmp := a.OccurredAt.Compare(b.OccurredAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.EntityID, b.EntityID)
	})
	return canonical
}

func (r *Runner) dispatchNextPending(ctx context.Context, state *State) error {
	groups, err := r.admin.ListSessionGroups(ctx, projectName)
	if err != nil {
		return err
	}
	for _, group := range groups {
		for _, session := range group.Sessions {
			if session.Busy {
				return nil
			}
		}
	}

	cluster := pickNextPendingCluster(state)
	if cluster == nil {
		return nil
	}
	prompt := buildPrompt(state, cluster)
	if prompt == "" {
		cluster.Pending = nil
		return nil
	}

	err = r.admin.SubmitPrompt(ctx, SubmitPromptRequest{
		Project:    projectName,
		SessionKey: r.cfg.SessionKey,
		SessionID:  cluster.SessionID,
		Prompt:     prompt,
	})
	if err != nil {
		if isBusyError(err) {
			return nil
		}
		return err
	}
	cluster.Pending = nil
	cluster.LastSubmittedAt = r.now()
	return nil
}

func pickNextPendingCluster(state *State) *TrackedCluster {
	var clusters []*TrackedCluster
	for _, cluster := range state.Clusters {
		if len(cluster.Pending) == 0 {
			continue
		}
		clusters = append(clusters, cluster)
	}
	slices.SortFunc(clusters, func(a, b *TrackedCluster) int {
		acomment := clusterHasComments(a)
		bcomment := clusterHasComments(b)
		switch {
		case acomment && !bcomment:
			return -1
		case !acomment && bcomment:
			return 1
		}
		afirst := firstPendingAt(a)
		bfirst := firstPendingAt(b)
		if cmp := afirst.Compare(bfirst); cmp != 0 {
			return cmp
		}
		if cmp := a.CreatedAt.Compare(b.CreatedAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(clusters) == 0 {
		return nil
	}
	return clusters[0]
}

func clusterHasComments(cluster *TrackedCluster) bool {
	for _, event := range cluster.Pending {
		if event.Kind == "comment" && len(event.Comments) > 0 {
			return true
		}
	}
	return false
}

func firstPendingAt(cluster *TrackedCluster) time.Time {
	if len(cluster.Pending) == 0 {
		return time.Time{}
	}
	first := cluster.Pending[0].OccurredAt
	for _, event := range cluster.Pending[1:] {
		if event.OccurredAt.Before(first) {
			first = event.OccurredAt
		}
	}
	return first
}

func buildPrompt(state *State, cluster *TrackedCluster) string {
	if cluster == nil || len(cluster.Pending) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Continue handling this Forgejo workflow in the current shared session.\n\n")
	b.WriteString("Tracked items in this session:\n")

	memberEntities := make([]*TrackedEntity, 0, len(cluster.Members))
	for _, memberID := range cluster.Members {
		if entity := state.Entities[memberID]; entity != nil {
			memberEntities = append(memberEntities, entity)
		}
	}
	slices.SortFunc(memberEntities, func(a, b *TrackedEntity) int {
		if a.Kind != b.Kind {
			if a.Kind == "issue" {
				return -1
			}
			return 1
		}
		if a.RepoFullName != b.RepoFullName {
			return strings.Compare(a.RepoFullName, b.RepoFullName)
		}
		switch {
		case a.Number < b.Number:
			return -1
		case a.Number > b.Number:
			return 1
		default:
			return 0
		}
	})
	for _, entity := range memberEntities {
		fmt.Fprintf(&b, "- %s %s#%d: %s\n", strings.ToUpper(entity.Kind), entity.RepoFullName, entity.Number, entity.Title)
	}

	var initEvents []PendingEvent
	var commentEvents []PendingEvent
	for _, event := range cluster.Pending {
		if event.Kind == "init" {
			initEvents = append(initEvents, event)
		} else if len(event.Comments) > 0 {
			commentEvents = append(commentEvents, event)
		}
	}
	slices.SortFunc(initEvents, comparePendingEvents)
	slices.SortFunc(commentEvents, comparePendingEvents)

	if len(initEvents) > 0 {
		b.WriteString("\nNew tracked items:\n")
		for _, event := range initEvents {
			fmt.Fprintf(&b, "\n[%s %s#%d]\n", strings.ToUpper(event.Entity.Kind), event.Entity.RepoFullName, event.Entity.Number)
			fmt.Fprintf(&b, "Title: %s\nURL: %s\n", event.Entity.Title, event.Entity.URL)
			if strings.TrimSpace(event.Entity.Body) != "" {
				fmt.Fprintf(&b, "Body:\n%s\n", strings.TrimSpace(event.Entity.Body))
			}
			if len(event.Comments) == 0 {
				b.WriteString("Existing comments: none\n")
			} else {
				b.WriteString("Existing comments:\n")
				for _, comment := range event.Comments {
					fmt.Fprintf(&b, "- [%s] %s: %s\n", comment.CreatedAt.Format(time.RFC3339), comment.Author, strings.TrimSpace(comment.Body))
				}
			}
		}
	}

	if len(commentEvents) > 0 {
		b.WriteString("\nNew comments since the last handled batch:\n")
		for _, event := range commentEvents {
			fmt.Fprintf(&b, "\n[%s %s#%d]\n", strings.ToUpper(event.Entity.Kind), event.Entity.RepoFullName, event.Entity.Number)
			for _, comment := range event.Comments {
				fmt.Fprintf(&b, "- [%s] %s: %s\n", comment.CreatedAt.Format(time.RFC3339), comment.Author, strings.TrimSpace(comment.Body))
			}
		}
	}

	b.WriteString("\nKeep related issues and PRs in this same session and continue the workflow from the latest state.")
	return b.String()
}

func comparePendingEvents(a, b PendingEvent) int {
	if cmp := a.OccurredAt.Compare(b.OccurredAt); cmp != 0 {
		return cmp
	}
	return strings.Compare(a.EntityID, b.EntityID)
}

func snapshotEntity(entity *TrackedEntity) PendingEntity {
	return PendingEntity{
		Kind:         entity.Kind,
		RepoFullName: entity.RepoFullName,
		Number:       entity.Number,
		Title:        entity.Title,
		Body:         entity.Body,
		URL:          entity.URL,
	}
}

func snapshotComments(comments []ForgejoComment) []PendingComment {
	out := make([]PendingComment, 0, len(comments))
	for _, comment := range comments {
		out = append(out, PendingComment{
			ID:        comment.ID,
			Author:    comment.User.Login,
			Body:      comment.Body,
			URL:       comment.HTMLURL,
			CreatedAt: comment.CreatedAt,
		})
	}
	return out
}

func ensureMember(cluster *TrackedCluster, entityID string) {
	for _, memberID := range cluster.Members {
		if memberID == entityID {
			return
		}
	}
	cluster.Members = append(cluster.Members, entityID)
}

func canonicalUnique(state *State, ids []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, id := range ids {
		id = canonicalClusterID(state, id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		if _, ok := state.Clusters[id]; !ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func canonicalClusterID(state *State, id string) string {
	for id != "" {
		next, ok := state.Aliases[id]
		if !ok || next == "" || next == id {
			return id
		}
		id = next
	}
	return ""
}

func (r *Runner) canonicalClusterID(state *State, id string) string {
	return canonicalClusterID(state, id)
}

func entityID(owner, repo string, number int64) string {
	return fmt.Sprintf("%s/%s#%d", owner, repo, number)
}

func sessionName(entity *TrackedEntity) string {
	repo := strings.ReplaceAll(entity.Repo, " ", "-")
	repo = strings.ReplaceAll(repo, "/", "-")
	return fmt.Sprintf("forgejo-%s-%d", repo, entity.Number)
}

func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "busy")
}
