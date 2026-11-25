package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageUpdateChecksReleaseBeforeInstalling(t *testing.T) {
	const tag = "build-123456789a"
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	entry, _ := z.Create("perf-lab.exe")
	entry.Write([]byte("invalid binary"))
	z.Close()
	sum := sha256.Sum256(archive.Bytes())
	asset := releaseAsset{Name: windowsAsset, State: "uploaded", Size: int64(archive.Len()), Digest: "sha256:" + hex.EncodeToString(sum[:])}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			json.NewEncoder(w).Encode(appRelease{Tag: tag, Assets: []releaseAsset{asset}})
			return
		}
		if r.URL.Path != "/download/"+tag+"/"+windowsAsset {
			http.NotFound(w, r)
			return
		}
		requests++
		w.Write(archive.Bytes())
	}))
	defer srv.Close()
	dir := t.TempDir()
	check := func(current string) (stagedUpdate, error) {
		return stageUpdate(context.Background(), srv.Client(), srv.URL+"/latest", srv.URL+"/download/", filepath.Join(dir, "perf-lab.exe"), current, nil)
	}
	s, err := check(tag)
	if err != nil || s.dir != "" || requests != 0 {
		t.Fatal(s, err, requests)
	}
	if _, err = check(""); err == nil || !strings.Contains(err.Error(), "опубликованной сборке") {
		t.Fatal(err)
	}
	asset.Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err = check(""); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal(files, err)
	}
}
func TestProgramUpdateIsUnavailableDuringSQLWork(t *testing.T) {
	m := newUI(t.TempDir())
	hasUpdate := func() bool {
		for _, c := range m.controls() {
			if c.key == "update" {
				return true
			}
		}
		return false
	}
	if !hasUpdate() {
		t.Fatal("update missing")
	}
	m.running = true
	if hasUpdate() {
		t.Fatal("updating while running")
	}
	m.running = false
	m.restorePicker = &restorePicker{}
	if hasUpdate() {
		t.Fatal("update inside restore dialog")
	}
}

func TestStartupUpdateWaitsForSQLThenStartsWithoutInput(t *testing.T) {
	m := newUI(t.TempDir())
	m.checkOnOpen, m.running = true, true
	if m.Init() == nil {
		t.Fatal("startup check missing")
	}
	result, _ := m.Update(startupRelease{tag: "build-234567890a"})
	m = result.(ui)
	if m.update != nil || !m.pendingUpdate {
		t.Fatal("update interfered with SQL")
	}
	result, cmd := m.Update(streamMsg{value: done{file: "report.json"}})
	m = result.(ui)
	if m.update == nil || cmd == nil || m.running || m.pendingUpdate || m.tab != 2 {
		t.Fatal("update did not start after report was saved")
	}
	m.update.cancel()
}

func TestStartupCheckOfflineCurrentAndOpenForm(t *testing.T) {
	m := newUI(t.TempDir())
	m.checkOnOpen = true
	out, cmd := m.Update(startupRelease{err: context.DeadlineExceeded})
	if out.(ui).update != nil || out.(ui).modal() || cmd != nil {
		t.Fatal("network error blocked launch")
	}
	out, cmd = m.Update(startupRelease{tag: currentRelease})
	if cmd != nil || out.(ui).update != nil {
		t.Fatal("already current")
	}
	m.form = &profileForm{}
	out, _ = m.Update(startupRelease{tag: "build-234567890a"})
	m = out.(ui)
	if !m.pendingUpdate || m.update != nil || m.form == nil {
		t.Fatal("interrupted profile editing")
	}
}
