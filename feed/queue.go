package feed

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func baseDir(root string, c Config) string {
	return filepath.Join(root, "local", "feed-queue", strconv.FormatInt(c.ID, 10))
}

type Session struct {
	Root, ID, dir string
	Config        Config
	sequence      int
	mu            sync.Mutex
}

func Begin(root string, c Config) (*Session, error) {
	var id [10]byte
	if _, e := rand.Read(id[:]); e != nil {
		return nil, e
	}
	run := time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(id[:])
	dir := filepath.Join(baseDir(root, c), run)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	return &Session{Root: root, ID: run, dir: dir, Config: c}, nil
}

var auth = regexp.MustCompile(`(?i)\b(password|pwd|access_token|authorization)\s*[:=]\s*("[^"]*"|'[^']*'|[^;\s,]+)`)
var ghToken = regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9_]{12,}|github_pat_[A-Za-z0-9_]{12,}|Bearer\s+[A-Za-z0-9._~-]+)`)

func clean(s string) string {
	s = ghToken.ReplaceAllString(s, "[скрыто]")
	s = auth.ReplaceAllString(s, "$1=[скрыто]")
	for _, k := range []string{"PERFLAB_ERP_PASSWORD", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			s = strings.ReplaceAll(s, v, "[скрыто]")
		}
	}
	return s
}
func scrub(v any) any {
	switch x := v.(type) {
	case string:
		return clean(x)
	case []any:
		for i := range x {
			x[i] = scrub(x[i])
		}
	case map[string]any:
		result := make(map[string]any, len(x))
		for k, v := range x {
			result[clean(k)] = scrub(v)
		}
		return result
	}
	return v
}
func sanitized(v any) (any, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var val any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e = d.Decode(&val); e != nil {
		return nil, e
	}
	return scrub(val), nil
}
func (s *Session) Event(kind string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	value, e := sanitized(v)
	if e != nil {
		return e
	}
	data := map[string]any{"seq": s.sequence, "utc": time.Now().UTC(), "type": kind, "data": value}
	return lab.SaveJSON(filepath.Join(s.dir, fmt.Sprintf("e-%09d.json", s.sequence)), data)
}
func (s *Session) Report(v any) error {
	value, e := sanitized(v)
	if e != nil {
		return e
	}
	return lab.SaveJSON(filepath.Join(s.dir, "report.json"), value)
}

type batch struct {
	Files []string
	Name  string
}
type uploader func(context.Context, string, []byte) error

const partSize = 480 * 1024

func uploadFile(ctx context.Context, path string, b []byte, put uploader) error {
	if len(b) <= partSize {
		return put(ctx, path, b)
	}
	var parts []string
	for start := 0; start < len(b); start += partSize {
		key := fmt.Sprintf("%s.part-%05d", path, len(parts)+1)
		if e := put(ctx, key, b[start:min(start+partSize, len(b))]); e != nil {
			return e
		}
		parts = append(parts, filepath.Base(key))
	}
	m, _ := json.Marshal(map[string]any{"bytes": len(b), "encoding": "concatenate parts in order", "parts": parts})
	return put(ctx, path+".manifest.json", m)
}
func drain(ctx context.Context, root string, c Config, put uploader) error {
	dir := baseDir(root, c)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	lock := flock.New(filepath.Join(dir, "sync.lock"))
	ok, e := lock.TryLock()
	if e != nil {
		return e
	}
	if !ok {
		return errors.New("очередь уже отправляется")
	}
	defer lock.Unlock()
	dirs, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		if e := drainRun(ctx, filepath.Join(dir, d.Name()), "runs/"+d.Name()+"/", put); e != nil {
			return e
		}
	}
	return nil
}
func drainRun(ctx context.Context, dir, prefix string, put uploader) error {
	manifest := filepath.Join(dir, "batch.json")
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		var page batch
		b, e := os.ReadFile(manifest)
		if e == nil {
			if e = json.Unmarshal(b, &page); e != nil {
				return e
			}
		} else if os.IsNotExist(e) {
			files, e := filepath.Glob(filepath.Join(dir, "e-*.json"))
			if e != nil {
				return e
			}
			sort.Strings(files)
			if len(files) == 0 {
				break
			}
			for _, f := range files[:min(64, len(files))] {
				page.Files = append(page.Files, filepath.Base(f))
			}
			page.Name = "events-" + strings.TrimSuffix(page.Files[0][2:], ".json") + "-" + page.Files[len(page.Files)-1][2:]
			if e = lab.SaveJSON(manifest, page); e != nil {
				return e
			}
		} else {
			return e
		}
		// The sent marker makes local cleanup retryable after a crash.
		sent := manifest + ".sent"
		if _, e = os.Stat(sent); os.IsNotExist(e) {
			events := make([]json.RawMessage, 0, len(page.Files))
			for _, p := range page.Files {
				v, e := os.ReadFile(filepath.Join(dir, p))
				if e != nil {
					return e
				}
				events = append(events, v)
			}
			body, e := json.Marshal(events)
			if e != nil {
				return e
			}
			if e = uploadFile(ctx, prefix+page.Name, body, put); e != nil {
				return e
			}
			if e = lab.SaveJSON(sent, page.Name); e != nil {
				return e
			}
		} else if e != nil {
			return e
		} else {
			var ack string
			v, e := os.ReadFile(sent)
			if e != nil {
				return e
			}
			if e = json.Unmarshal(v, &ack); e != nil {
				return e
			}
			if ack != page.Name {
				if e = os.Remove(sent); e != nil {
					return e
				}
				continue
			}
		}
		for _, p := range page.Files {
			if e = os.Remove(filepath.Join(dir, p)); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
		if e = os.Remove(manifest); e != nil {
			return e
		}
		if e = os.Remove(sent); e != nil {
			return e
		}
	}
	report := filepath.Join(dir, "report.json")
	b, e := os.ReadFile(report)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if e = uploadFile(ctx, prefix+"report.json", b, put); e != nil {
		return e
	}
	return os.Remove(report)
}
func Sync(ctx context.Context, root string, cfg Config) error {
	c, e := newClient(ctx)
	if e != nil {
		return e
	}
	return drain(ctx, root, cfg, func(ctx context.Context, path string, b []byte) error { return c.put(ctx, cfg, path, b) })
}
