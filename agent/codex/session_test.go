package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZacharyJia/cx-connect/core"
)

func TestCodexSessionSendUsesStdinForMultilinePrompt(t *testing.T) {
	binDir := t.TempDir()
	logDir := t.TempDir()
	workDir := t.TempDir()

	script := filepath.Join(binDir, "codex")
	scriptBody := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$CX_TEST_LOGDIR/args.txt"
cat > "$CX_TEST_LOGDIR/stdin.txt"
printf '%s\n' '{"type":"thread.started","thread_id":"thread-test"}'
printf '%s\n' '{"type":"turn.completed"}'
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CX_TEST_LOGDIR", logDir)

	cs, err := newCodexSession(context.Background(), workDir, "", "", "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}
	defer cs.Close()

	prompt := "第一行\nsecond line\nthird line"
	if err := cs.Send(prompt, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitForResultEvent(t, cs.Events())
	waitForFileContent(t, filepath.Join(logDir, "stdin.txt"), prompt)

	stdinData, err := os.ReadFile(filepath.Join(logDir, "stdin.txt"))
	if err != nil {
		t.Fatalf("read stdin log: %v", err)
	}
	if got := string(stdinData); got != prompt {
		t.Fatalf("stdin prompt mismatch\nwant: %q\ngot:  %q", prompt, got)
	}

	argsData, err := os.ReadFile(filepath.Join(logDir, "args.txt"))
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(argsData)), "\n")
	want := []string{"exec", "--json", "--skip-git-repo-check", "--cd", workDir, "-"}
	if len(args) != len(want) {
		t.Fatalf("unexpected args length\nwant: %q\ngot:  %q", want, args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg %d mismatch\nwant: %q\ngot:  %q", i, want[i], args[i])
		}
	}
}

func TestCodexSessionResumeUsesStdinPrompt(t *testing.T) {
	binDir := t.TempDir()
	logDir := t.TempDir()
	workDir := t.TempDir()

	script := filepath.Join(binDir, "codex")
	scriptBody := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$CX_TEST_LOGDIR/args.txt"
cat > "$CX_TEST_LOGDIR/stdin.txt"
printf '%s\n' '{"type":"turn.completed"}'
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CX_TEST_LOGDIR", logDir)

	cs, err := newCodexSession(context.Background(), workDir, "", "", "thread-123", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}
	defer cs.Close()

	prompt := "line one\nline two"
	if err := cs.Send(prompt, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitForResultEvent(t, cs.Events())
	waitForFileContent(t, filepath.Join(logDir, "stdin.txt"), prompt)

	stdinData, err := os.ReadFile(filepath.Join(logDir, "stdin.txt"))
	if err != nil {
		t.Fatalf("read stdin log: %v", err)
	}
	if got := string(stdinData); got != prompt {
		t.Fatalf("stdin prompt mismatch\nwant: %q\ngot:  %q", prompt, got)
	}

	argsData, err := os.ReadFile(filepath.Join(logDir, "args.txt"))
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(argsData)), "\n")
	want := []string{"exec", "resume", "--json", "--skip-git-repo-check", "thread-123", "-"}
	if len(args) != len(want) {
		t.Fatalf("unexpected args length\nwant: %q\ngot:  %q", want, args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg %d mismatch\nwant: %q\ngot:  %q", i, want[i], args[i])
		}
	}
}

func waitForResultEvent(t *testing.T, events <-chan core.Event) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-events:
			if evt.Type == core.EventResult {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for result event")
		}
	}
}

func waitForFileContent(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && string(data) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("timed out waiting for %s content\nwant: %q\ngot:  %q", path, want, string(data))
}
