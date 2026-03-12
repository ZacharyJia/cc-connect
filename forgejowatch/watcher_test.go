package forgejowatch

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseLinkedReferences(t *testing.T) {
	text := "This PR fixes #2 and closes team/backend#8."
	refs := parseLinkedReferences(text, "acme", "web")
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %v", refs)
	}
	if refs[0] != "acme/web#2" || refs[1] != "team/backend#8" {
		t.Fatalf("unexpected refs: %v", refs)
	}
}

func TestSyncDefersInitializationWhileAnySessionBusy(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	store := &StateStore{
		path: t.TempDir() + "/state.json",
		state: &State{
			Entities: make(map[string]*TrackedEntity),
			Clusters: make(map[string]*TrackedCluster),
			Aliases:  make(map[string]string),
		},
	}
	forgejo := &fakeForgejo{
		issues: []ForgejoIssue{
			{
				Number:    12,
				Title:     "Fix login bug",
				Body:      "Needs investigation",
				HTMLURL:   "https://forgejo.example/acme/app/issues/12",
				UpdatedAt: now,
				Repository: ForgejoRepository{
					FullName: "acme/app",
					Name:     "app",
					Owner:    ForgejoOwner{Login: "acme"},
				},
			},
		},
	}
	admin := &fakeAdmin{
		groups: []AdminSessionGroup{
			{SessionKey: "telegram:ops:1", Sessions: []AdminSessionSummary{{ID: "existing", Busy: true}}},
		},
	}

	runner := NewRunnerWithClients(Config{
		Name:       "ops",
		SessionKey: "telegram:ops:1",
	}, store, forgejo, admin)
	runner.now = func() time.Time { return now }

	if err := runner.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(admin.created) != 1 {
		t.Fatalf("expected 1 created session, got %d", len(admin.created))
	}
	if len(admin.prompts) != 0 {
		t.Fatalf("expected no prompt while busy, got %d", len(admin.prompts))
	}

	admin.groups = []AdminSessionGroup{
		{
			SessionKey: "telegram:ops:1",
			Sessions: []AdminSessionSummary{
				{ID: "existing", Busy: false},
				{ID: admin.created[0].Session.ID, Busy: false},
			},
		},
	}

	if err := runner.Sync(context.Background()); err != nil {
		t.Fatalf("Sync second pass: %v", err)
	}
	if len(admin.prompts) != 1 {
		t.Fatalf("expected one prompt after idle, got %d", len(admin.prompts))
	}
}

func TestPickNextPendingClusterPrefersComments(t *testing.T) {
	state := &State{
		Clusters: map[string]*TrackedCluster{
			"comment": {
				ID: "comment",
				Pending: []PendingEvent{
					{Kind: "comment", OccurredAt: time.Date(2026, 3, 11, 10, 5, 0, 0, time.UTC), Comments: []PendingComment{{ID: 1}}},
				},
			},
			"init": {
				ID: "init",
				Pending: []PendingEvent{
					{Kind: "init", OccurredAt: time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)},
				},
			},
		},
	}
	runner := NewRunnerWithClients(Config{
		Username: "zachary",
	}, &StateStore{state: state}, &fakeForgejo{}, &fakeAdmin{})
	cluster := runner.pickNextPendingCluster(state)
	if cluster == nil || cluster.ID != "comment" {
		t.Fatalf("unexpected cluster pick: %+v", cluster)
	}
}

func TestSyncSkipsSelfOnlyPullActivityUntilExternalComment(t *testing.T) {
	now := time.Date(2026, 3, 12, 9, 0, 0, 0, time.UTC)
	store := &StateStore{
		path: t.TempDir() + "/state.json",
		state: &State{
			Entities: make(map[string]*TrackedEntity),
			Clusters: make(map[string]*TrackedCluster),
			Aliases:  make(map[string]string),
		},
	}
	forgejo := &fakeForgejo{
		pulls: []ForgejoIssue{
			{
				Number:    18,
				Title:     "Add watcher improvements",
				Body:      "Implements the watcher change",
				HTMLURL:   "https://forgejo.example/acme/app/pulls/18",
				UpdatedAt: now,
				Repository: ForgejoRepository{
					FullName: "acme/app",
					Name:     "app",
					Owner:    ForgejoOwner{Login: "acme"},
				},
			},
		},
		comments: map[string][]ForgejoComment{},
	}
	admin := &fakeAdmin{}
	runner := NewRunnerWithClients(Config{
		Name:       "ops",
		Username:   "zachary",
		SessionKey: "telegram:ops:1",
	}, store, forgejo, admin)
	runner.now = func() time.Time { return now }

	if err := runner.Sync(context.Background()); err != nil {
		t.Fatalf("Sync first pass: %v", err)
	}
	if len(admin.prompts) != 0 {
		t.Fatalf("expected no prompt for self-created pull without external comments, got %d", len(admin.prompts))
	}

	key := entityID("acme", "app", 18)
	forgejo.comments[key] = []ForgejoComment{
		{
			ID:        1,
			Body:      "I'll handle this shortly.",
			CreatedAt: now.Add(time.Minute),
			User:      ForgejoUser{Login: "zachary"},
		},
	}
	runner.now = func() time.Time { return now.Add(time.Minute) }
	if err := runner.Sync(context.Background()); err != nil {
		t.Fatalf("Sync second pass: %v", err)
	}
	if len(admin.prompts) != 0 {
		t.Fatalf("expected no prompt for self-only comment, got %d", len(admin.prompts))
	}

	forgejo.comments[key] = append(forgejo.comments[key], ForgejoComment{
		ID:        2,
		Body:      "Please update the tests.",
		CreatedAt: now.Add(2 * time.Minute),
		User:      ForgejoUser{Login: "reviewer"},
	})
	runner.now = func() time.Time { return now.Add(2 * time.Minute) }
	if err := runner.Sync(context.Background()); err != nil {
		t.Fatalf("Sync third pass: %v", err)
	}
	if len(admin.prompts) != 1 {
		t.Fatalf("expected one prompt after external comment, got %d", len(admin.prompts))
	}
	if !strings.Contains(admin.prompts[0].Prompt, "zachary") || !strings.Contains(admin.prompts[0].Prompt, "reviewer") {
		t.Fatalf("expected bundled prompt to contain self and external comments, got %q", admin.prompts[0].Prompt)
	}
}

type fakeForgejo struct {
	issues    []ForgejoIssue
	pulls     []ForgejoIssue
	comments  map[string][]ForgejoComment
	pullByKey map[string]ForgejoIssue
}

func (f *fakeForgejo) ListAssignedIssues(ctx context.Context, state string) ([]ForgejoIssue, error) {
	return append([]ForgejoIssue(nil), f.issues...), nil
}

func (f *fakeForgejo) ListCreatedPulls(ctx context.Context, state string) ([]ForgejoIssue, error) {
	return append([]ForgejoIssue(nil), f.pulls...), nil
}

func (f *fakeForgejo) ListComments(ctx context.Context, owner, repo string, number int64) ([]ForgejoComment, error) {
	key := entityID(owner, repo, number)
	return append([]ForgejoComment(nil), f.comments[key]...), nil
}

func (f *fakeForgejo) GetPull(ctx context.Context, owner, repo string, number int64) (ForgejoIssue, error) {
	key := entityID(owner, repo, number)
	if f.pullByKey != nil {
		if issue, ok := f.pullByKey[key]; ok {
			return issue, nil
		}
	}
	return ForgejoIssue{}, nil
}

type fakeAdmin struct {
	createSeq int
	created   []CreateSessionResponse
	groups    []AdminSessionGroup
	prompts   []SubmitPromptRequest
}

func (f *fakeAdmin) CreateSession(ctx context.Context, req CreateSessionRequest) (CreateSessionResponse, error) {
	f.createSeq++
	resp := CreateSessionResponse{SessionKey: req.SessionKey}
	resp.Session.ID = "session-" + time.Now().Format("150405") + "-" + string(rune('a'+f.createSeq))
	f.created = append(f.created, resp)
	return resp, nil
}

func (f *fakeAdmin) ListSessionGroups(ctx context.Context, project string) ([]AdminSessionGroup, error) {
	return append([]AdminSessionGroup(nil), f.groups...), nil
}

func (f *fakeAdmin) SubmitPrompt(ctx context.Context, req SubmitPromptRequest) error {
	f.prompts = append(f.prompts, req)
	return nil
}
