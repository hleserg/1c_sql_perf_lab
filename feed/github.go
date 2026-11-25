// Package feed sends run artifacts to an explicitly selected private repository.
package feed

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	Repo string
	ID   int64
}
type client struct {
	api, token string
	http       *http.Client
}

var validRepo = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)

func newClient(ctx context.Context) (client, error) {
	cmd := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com")
	out, e := cmd.Output()
	if e != nil {
		return client{}, errors.New("авторизация недоступна · проверь gh auth status --hostname github.com")
	}
	return client{"https://api.github.com", strings.TrimSpace(string(out)), &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c client) request(ctx context.Context, method, path string, payload []byte, dst any) (int, error) {
	req, e := http.NewRequestWithContext(ctx, method, c.api+path, bytes.NewReader(payload))
	if e != nil {
		return 0, e
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, e := c.http.Do(req)
	if e != nil {
		return 0, e
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return r.StatusCode, fmt.Errorf("GitHub HTTP %d · %s", r.StatusCode, http.StatusText(r.StatusCode))
	}
	if dst != nil {
		e = json.NewDecoder(io.LimitReader(r.Body, 3<<20)).Decode(dst)
	}
	return r.StatusCode, e
}
func (c client) verify(ctx context.Context, cfg Config) (int64, string, error) {
	if !validRepo.MatchString(cfg.Repo) {
		return 0, "", errors.New("ожидается owner/repo")
	}
	var meta struct {
		ID            int64
		FullName      string `json:"full_name"`
		Private       bool
		Visibility    string
		DefaultBranch string `json:"default_branch"`
		Permissions   struct{ Push bool }
	}
	_, e := c.request(ctx, "GET", "/repos/"+cfg.Repo, nil, &meta)
	if e != nil {
		return 0, "", e
	}
	if meta.Visibility != "private" || !meta.Private || !meta.Permissions.Push || !strings.EqualFold(meta.FullName, cfg.Repo) || meta.ID <= 0 || cfg.ID != 0 && cfg.ID != meta.ID {
		return 0, "", errors.New("назначение должно быть тем же приватным репозиторием с правом записи")
	}
	if meta.DefaultBranch == "" {
		return 0, "", errors.New("репозиторий пуст · добавь README")
	}
	return meta.ID, meta.DefaultBranch, nil
}
func cfgPath(root string) string { return filepath.Join(root, "local", "github-feed.json") }
func Load(root string) (cfg Config, e error) {
	b, e := os.ReadFile(cfgPath(root))
	if errors.Is(e, os.ErrNotExist) {
		return cfg, nil
	}
	if e == nil {
		e = json.Unmarshal(b, &cfg)
	}
	if e == nil && (cfg.ID <= 0 || !validRepo.MatchString(cfg.Repo)) {
		e = errors.New("повреждена привязка GitHub · выполни -github owner/repo")
	}
	return
}
func Configure(ctx context.Context, root, repo string) error {
	if repo == "off" {
		e := os.Remove(cfgPath(root))
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		return e
	}
	c, e := newClient(ctx)
	if e != nil {
		return e
	}
	cfg := Config{Repo: repo}
	id, branch, e := c.verify(ctx, cfg)
	if e != nil {
		return e
	}
	if _, e = c.request(ctx, "GET", "/repos/"+repo+"/branches/"+url.PathEscape(branch), nil, nil); e != nil {
		return fmt.Errorf("проверь наличие первого коммита в %s: %w", branch, e)
	}
	cfg.ID = id
	return lab.SaveJSON(cfgPath(root), cfg)
}
func gitSHA(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d%c", len(b), 0)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func (c client) put(ctx context.Context, cfg Config, path string, b []byte) error {
	_, branch, e := c.verify(ctx, cfg)
	if e != nil {
		return e
	}
	// An immutable file per batch makes a retry after a lost HTTP response idempotent.
	ep := "/repos/" + cfg.Repo + "/contents/" + path
	body, _ := json.Marshal(map[string]string{"message": "Perf Lab · " + path, "branch": branch, "content": base64.StdEncoding.EncodeToString(b)})
	code, e := c.request(ctx, "PUT", ep, body, nil)
	if e == nil {
		return nil
	}
	if code != 409 && code != 422 {
		return e
	}
	var existing struct{ SHA string }
	_, ge := c.request(ctx, "GET", ep+"?ref="+url.QueryEscape(branch), nil, &existing)
	if ge == nil && existing.SHA == gitSHA(b) {
		return nil
	}
	return e
}
