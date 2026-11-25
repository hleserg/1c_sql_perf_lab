package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"
)

const releaseAPI = "https://api.github.com/repos/hleserg/1c_sql_perf_lab/releases/latest"
const releaseDownloads = "https://github.com/hleserg/1c_sql_perf_lab/releases/download/"
const windowsAsset = "1c_sql_perf_lab-windows-x64.zip"
const maxUpdateZip = 64 << 20
const maxUpdateExe = 128 << 20

var releaseTag = regexp.MustCompile("^build-[0-9a-f]{10}$")

func binaryRelease(info *debug.BuildInfo) string {
	if info == nil {
		return ""
	}
	var revision string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				return ""
			}
		}
	}
	if len(revision) >= 10 && releaseTag.MatchString("build-"+revision[:10]) {
		return "build-" + revision[:10]
	}
	return ""
}

var currentRelease = func() string { info, _ := debug.ReadBuildInfo(); return binaryRelease(info) }()

type releaseAsset struct {
	Name   string
	Size   int64
	Digest string
	State  string
}
type appRelease struct {
	Tag               string `json:"tag_name"`
	Draft, Prerelease bool
	Assets            []releaseAsset
}
type stagedUpdate struct {
	dir, exe, tag, digest string
	showResults           bool
}

func (s stagedUpdate) cleanup() {
	if s.dir != "" {
		os.RemoveAll(s.dir)
	}
}

func fetchUpdate(ctx context.Context, client *http.Client, url string, limit int64, progress io.Writer) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "1c-sql-perf-lab-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub: %s", resp.Status)
	}
	if resp.ContentLength > limit {
		return nil, errors.New("размер ответа превышает лимит")
	}
	body := io.LimitReader(resp.Body, limit+1)
	if progress != nil {
		body = io.TeeReader(body, progress)
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("размер ответа превышает лимит")
	}
	return b, nil
}
func latestRelease(ctx context.Context, client *http.Client, api string) (appRelease, error) {
	var r appRelease
	meta, err := fetchUpdate(ctx, client, api, 2<<20, nil)
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(meta, &r); err != nil {
		return r, err
	}
	if r.Draft || r.Prerelease || !releaseTag.MatchString(r.Tag) {
		return r, errors.New("неизвестный формат последнего стабильного релиза")
	}
	return r, nil
}

type startupRelease struct {
	tag string
	err error
}

func checkStartupRelease() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	r, err := latestRelease(ctx, http.DefaultClient, releaseAPI)
	return startupRelease{r.Tag, err}
}

func (m *ui) startUpdate() tea.Cmd {
	m.pendingUpdate, m.checkOnOpen = false, false
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	task := &updateTask{cancel: cancel}
	m.update = task
	m.msg = "Проверяю последний релиз и загружаю обновление с GitHub…"
	return tea.Batch(func() tea.Msg { return downloadBuild(ctx, task) }, updatePulse(task))
}

func stageUpdate(ctx context.Context, client *http.Client, api, downloads, target, installed string, task *updateTask) (stagedUpdate, error) {
	r, err := latestRelease(ctx, client, api)
	if err != nil {
		return stagedUpdate{}, err
	}
	if r.Tag == installed {
		return stagedUpdate{tag: r.Tag}, nil
	}
	var asset releaseAsset
	for _, a := range r.Assets {
		if a.Name == windowsAsset && a.State == "uploaded" {
			asset = a
			break
		}
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(asset.Digest, "sha256:"))
	if err != nil || !strings.HasPrefix(asset.Digest, "sha256:") || len(digest) != sha256.Size || asset.Size <= 0 || asset.Size > maxUpdateZip {
		return stagedUpdate{}, errors.New("в релизе отсутствует Windows-архив с размером и SHA-256")
	}
	if task != nil {
		task.total.Store(asset.Size)
	}
	var progress io.Writer
	if task != nil {
		progress = task
	}
	data, err := fetchUpdate(ctx, client, downloads+r.Tag+"/"+windowsAsset, asset.Size, progress)
	if err != nil {
		return stagedUpdate{}, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != asset.Size || !bytes.Equal(sum[:], digest) {
		return stagedUpdate{}, errors.New("размер или SHA-256 архива не совпадает с релизом")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return stagedUpdate{}, err
	}
	var binary *zip.File
	for _, entry := range z.File {
		if entry.Name == "perf-lab.exe" {
			if binary != nil || !entry.Mode().IsRegular() || entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > maxUpdateExe {
				return stagedUpdate{}, errors.New("некорректный EXE в архиве")
			}
			binary = entry
		}
	}
	if binary == nil {
		return stagedUpdate{}, errors.New("в архиве нет perf-lab.exe")
	}
	dir, err := os.MkdirTemp(filepath.Dir(target), ".perf-lab-update-")
	if err != nil {
		return stagedUpdate{}, fmt.Errorf("папка программы: %w", err)
	}
	s := stagedUpdate{dir: dir, tag: r.Tag, exe: filepath.Join(dir, "perf-lab.exe")}
	ok := false
	defer func() {
		if !ok {
			s.cleanup()
		}
	}()
	reader, err := binary.Open()
	if err != nil {
		return stagedUpdate{}, err
	}
	defer reader.Close()
	file, err := os.OpenFile(s.exe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return stagedUpdate{}, err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(reader, maxUpdateExe+1))
	err = errors.Join(copyErr, file.Close())
	if err != nil {
		return stagedUpdate{}, err
	}
	if uint64(n) != binary.UncompressedSize64 {
		return stagedUpdate{}, errors.New("неверный размер EXE")
	}
	info, err := buildinfo.ReadFile(s.exe)
	if err != nil || info.Path != "github.com/hleserg/1c_sql_perf_lab" || binaryRelease(info) != r.Tag {
		return stagedUpdate{}, errors.New("программа в архиве не соответствует опубликованной сборке")
	}
	arch, osName := "", ""
	for _, setting := range info.Settings {
		switch setting.Key {
		case "GOOS":
			osName = setting.Value
		case "GOARCH":
			arch = setting.Value
		}
	}
	if osName != "windows" || arch != "amd64" {
		return stagedUpdate{}, errors.New("платформа EXE отличается от Windows x64")
	}
	if err = ctx.Err(); err != nil {
		return stagedUpdate{}, err
	}
	s.digest = hex.EncodeToString(hash.Sum(nil))
	ok = true
	return s, nil
}

type updateTick struct{ task *updateTask }

func updatePulse(t *updateTask) tea.Cmd {
	return tea.Tick(180*time.Millisecond, func(time.Time) tea.Msg { return updateTick{t} })
}
func (t *updateTask) Write(p []byte) (int, error) { t.received.Add(int64(len(p))); return len(p), nil }

type updateTask struct {
	received, total atomic.Int64
	cancel          context.CancelFunc
	installing      bool
}
type updateFetched struct {
	task   *updateTask
	stage  stagedUpdate
	target string
	err    error
}
type updateHandoff struct{ err error }

func downloadBuild(ctx context.Context, task *updateTask) updateFetched {
	result := updateFetched{task: task}
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		result.err = errors.New("автообновление доступно для Windows x64")
		return result
	}
	target, err := os.Executable()
	if err == nil {
		result.target = target
		client := &http.Client{Timeout: 2 * time.Minute}
		result.stage, err = stageUpdate(ctx, client, releaseAPI, releaseDownloads, target, currentRelease, task)
	}
	result.err = err
	return result
}

func (m ui) updateView(w int) string {
	t := m.update
	title := "Обновление программы"
	label := currentRelease
	if label == "" {
		label = "локальная сборка"
	}
	rows := []string{"Установлено: " + label, ""}
	if t.installing {
		rows = append(rows, "Обновление проверено. Перезапускаю программу…")
	} else if total := t.total.Load(); total > 0 {
		count := t.received.Load()
		rows = append(rows, "Загрузка релиза для Windows x64", progressBar(max(5, min(w-6, 68)), int(count), int(total)), fmt.Sprintf("%.1f из %.1f МБ", float64(count)/1e6, float64(total)/1e6))
	} else {
		rows = append(rows, "Ищу последний релиз на GitHub…")
	}
	return panel(title, rows, w, mint)
}
