package feed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueueRetryAndSecrets(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Repo: "owner/repo", ID: 23}
	s, e := Begin(root, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("PERFLAB_ERP_PASSWORD", "dummy-complex-test-value")
	if e = s.Event("error", map[string]string{"message": "SQL pwd=private-one; token dummy-complex-test-value"}); e != nil {
		t.Fatal(e)
	}
	var pendingKey string
	offline := func(_ context.Context, key string, b []byte) error { pendingKey = key; return errors.New("offline") }
	if drain(context.Background(), root, cfg, offline) == nil {
		t.Fatal("expected offline error")
	}
	if e = s.Event("finish", true); e != nil {
		t.Fatal(e)
	}
	if e = s.Report(map[string]string{"token": "github_pat_abcdef0123456789012345"}); e != nil {
		t.Fatal(e)
	}
	var keys []string
	ok := func(_ context.Context, key string, b []byte) error {
		keys = append(keys, key)
		if !json.Valid(b) || strings.Contains(string(b), "dummy-complex") || strings.Contains(string(b), "private-one") || strings.Contains(string(b), "github_pat_") {
			t.Fatal("invalid or unredacted payload")
		}
		return nil
	}
	if e = drain(context.Background(), root, cfg, ok); e != nil {
		t.Fatal(e)
	}
	if len(keys) != 3 || keys[0] != pendingKey || !strings.HasSuffix(keys[2], "report.json") {
		t.Fatal(keys)
	}
	if e = drain(context.Background(), root, cfg, ok); e != nil || len(keys) != 3 {
		t.Fatal(keys, e)
	}
}
func TestRefusePublicAndReplacement(t *testing.T) {
	puts := 0
	visibility := false
	id := int64(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			puts++
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"private": visibility, "visibility": "private", "id": id, "default_branch": "main", "full_name": "test/private", "permissions": map[string]bool{"push": true}})
	}))
	defer srv.Close()
	c := client{srv.URL, "fake", srv.Client()}
	cfg := Config{"test/private", 1}
	if e := c.put(context.Background(), cfg, "runs/a", []byte("data")); e == nil {
		t.Fatal("accepted public")
	}
	visibility = true
	id = 2
	if e := c.put(context.Background(), cfg, "runs/a", []byte("data")); e == nil || puts != 0 {
		t.Fatal("accepted replacement")
	}
	id = 1
	if e := c.put(context.Background(), cfg, "runs/a", []byte("data")); e != nil || puts != 1 {
		t.Fatal(e, puts)
	}
}
func TestLostResponseMatchesBlob(t *testing.T) {
	data := []byte("private test event")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT":
			w.WriteHeader(422)
		case strings.Contains(r.URL.Path, "contents"):
			json.NewEncoder(w).Encode(map[string]string{"sha": gitSHA(data)})
		default:
			json.NewEncoder(w).Encode(map[string]any{"private": true, "visibility": "private", "id": 42, "default_branch": "main", "full_name": "test/private", "permissions": map[string]bool{"push": true}})
		}
	}))
	defer srv.Close()
	c := client{srv.URL, "fake", srv.Client()}
	if e := c.put(context.Background(), Config{"test/private", 42}, "runs/a", data); e != nil {
		t.Fatal(e)
	}
	if e := c.put(context.Background(), Config{"test/private", 42}, "runs/a", []byte("other")); e == nil {
		t.Fatal("ignored collision")
	}
}
func TestQueueOrderAfterPartialCleanup(t *testing.T) {
	cfg := Config{"a/b", 9}
	root := t.TempDir()
	s, e := Begin(root, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Event("start", true); e != nil {
		t.Fatal(e)
	}
	first := "e-000000001.json"
	page := batch{Files: []string{first}, Name: "events-000000001-000000001.json"}
	b, _ := json.Marshal(page)
	os.WriteFile(filepath.Join(s.dir, "batch.json"), b, 0600)
	b, _ = json.Marshal(page.Name)
	os.WriteFile(filepath.Join(s.dir, "batch.json.sent"), b, 0600)
	os.Remove(filepath.Join(s.dir, first))
	if e = s.Event("end", true); e != nil {
		t.Fatal(e)
	}
	sent := 0
	e = drain(context.Background(), root, cfg, func(_ context.Context, p string, b []byte) error { sent++; return nil })
	if e != nil || sent != 1 {
		t.Fatal(sent, e)
	}
}
