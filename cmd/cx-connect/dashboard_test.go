package main

import "testing"

func TestNormalizePublicURL(t *testing.T) {
	got := normalizePublicURL("worker.example.com:6380/")
	want := "http://worker.example.com:6380"
	if got != want {
		t.Fatalf("normalizePublicURL() = %q, want %q", got, want)
	}
}

func TestReplaceURLHostPreservesListenPort(t *testing.T) {
	got := replaceURLHost("http://127.0.0.1:6380", "worker.example.com")
	want := "http://worker.example.com:6380"
	if got != want {
		t.Fatalf("replaceURLHost() = %q, want %q", got, want)
	}
}

func TestReplaceURLHostAllowsManualPort(t *testing.T) {
	got := replaceURLHost("http://127.0.0.1:6380", "worker.example.com:9000")
	want := "http://worker.example.com:9000"
	if got != want {
		t.Fatalf("replaceURLHost() = %q, want %q", got, want)
	}
}
