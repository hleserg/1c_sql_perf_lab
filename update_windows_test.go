package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run the actual Windows handoff against a locked executable. The new process
// receives the same -home argument, including spaces and non-ASCII characters.
func TestWindowsReplacementAndRelaunch(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "child.go")
	const code = `package main
import("os";"io";"path/filepath")
func main(){
 if len(os.Args)==2 && os.Args[1]=="hold"{io.Copy(io.Discard,os.Stdin);return}
 if len(os.Args)!=3 || os.Args[1]!="-home"{os.Exit(3)}
 if e:=os.WriteFile(filepath.Join(os.Args[2],"restarted"),[]byte(os.Getenv("PERFLAB_UPDATE_NOTICE")),0600);e!=nil{os.Exit(4)}
}`
	if err := os.WriteFile(source, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "child.exe")
	if b, err := exec.Command("go", "build", "-o", exe, source).CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"upgrade", "rollback"} {
		t.Run(scenario, func(t *testing.T) {
			install := filepath.Join(t.TempDir(), "Тест & Lab's folder")
			home := filepath.Join(install, "профиль стенда")
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			cfg := filepath.Join(home, "stand.json")
			if err := os.WriteFile(cfg, []byte("original-profile"), 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(install, "perf-lab.exe")
			if err := os.WriteFile(target, binary, 0700); err != nil {
				t.Fatal(err)
			}
			parent := exec.Command(target, "hold")
			pipe, err := parent.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = parent.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { pipe.Close(); parent.Process.Kill() })
			next := append(bytes.Clone(binary), '2')
			if scenario == "rollback" {
				next = []byte("invalid Windows executable")
			}
			stage, err := os.MkdirTemp(install, ".perf-lab-update-")
			if err != nil {
				t.Fatal(err)
			}
			s := stagedUpdate{dir: stage, exe: filepath.Join(stage, "perf-lab.exe"), tag: "build-123456789a"}
			if err = os.WriteFile(s.exe, next, 0700); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(next)
			s.digest = hex.EncodeToString(sum[:])
			cmd, err := updateCommand(s, target, home, parent.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() { finished <- cmd.Wait() }()
			timeout := time.After(30 * time.Second)
			for {
				if _, err = os.Stat(filepath.Join(stage, "ready")); err == nil {
					break
				}
				select {
				case err = <-finished:
					detail, _ := os.ReadFile(filepath.Join(stage, "error.txt"))
					t.Fatalf("no ready: %v: %s; %s", err, detail, &out)
				case <-timeout:
					cmd.Process.Kill()
					t.Fatal("handoff not ready")
				case <-time.After(80 * time.Millisecond):
				}
			}
			if b, _ := os.ReadFile(target); !bytes.Equal(b, binary) {
				t.Fatal("replaced before previous process exited")
			}
			pipe.Close()
			parent.Wait()
			select {
			case err = <-finished:
				if err != nil {
					t.Fatal(err, &out)
				}
			case <-time.After(35 * time.Second):
				cmd.Process.Kill()
				t.Fatal("updater did not finish")
			}
			deadline := time.Now().Add(12 * time.Second)
			var marker []byte
			for time.Now().Before(deadline) {
				marker, _ = os.ReadFile(filepath.Join(home, "restarted"))
				if len(marker) > 0 {
					break
				}
				time.Sleep(80 * time.Millisecond)
			}
			want, notice := next, "Обновлено до "+s.tag
			if scenario == "rollback" {
				want, notice = binary, "Обновление не установлено"
			}
			if b, _ := os.ReadFile(target); !bytes.Equal(b, want) || !strings.HasPrefix(string(marker), notice) {
				t.Fatalf("replacement or relaunch failed: %q", marker)
			}
			if b, _ := os.ReadFile(cfg); string(b) != "original-profile" {
				t.Fatal("profile changed")
			}
			if _, err = os.Stat(stage); !os.IsNotExist(err) {
				t.Fatal("stage not cleaned")
			}
		})
	}
}
