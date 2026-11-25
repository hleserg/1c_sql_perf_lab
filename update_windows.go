package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

//go:embed update_windows.ps1
var updateScript string

func updateCommand(s stagedUpdate, target, root string, parent int) (*exec.Cmd, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	words := utf16.Encode([]rune(updateScript))
	data := make([]byte, len(words)*2)
	for i, v := range words {
		binary.LittleEndian.PutUint16(data[i*2:], v)
	}
	cmd := exec.Command(filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(data))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Env = append(os.Environ(), "PERFLAB_UPDATE_DIR="+s.dir, "PERFLAB_UPDATE_TARGET="+target,
		"PERFLAB_UPDATE_ARGS="+windows.ComposeCommandLine([]string{"-home", root}), "PERFLAB_UPDATE_PARENT="+strconv.Itoa(parent),
		"PERFLAB_UPDATE_DIGEST="+s.digest, "PERFLAB_UPDATE_TAG="+s.tag)
	if s.showResults {
		cmd.Env = append(cmd.Env, "PERFLAB_UPDATE_RESULTS=1")
	}
	return cmd, nil
}

func handoffUpdate(s stagedUpdate, target, root string) error {
	cmd, err := updateCommand(s, target, root, os.Getpid())
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	for {
		select {
		case err := <-done:
			if b, e := os.ReadFile(filepath.Join(s.dir, "error.txt")); e == nil {
				return fmt.Errorf("подготовка перезапуска: %s", b)
			}
			return fmt.Errorf("помощник обновления завершился до перезапуска (%v)", err)
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(s.dir, "ready")); err == nil {
				return nil
			}
		case <-timer.C:
			cmd.Process.Kill()
			<-done
			return fmt.Errorf("Windows не подтвердила готовность к перезапуску")
		}
	}
}
