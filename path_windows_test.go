package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func sandboxEnvironmentKey(t *testing.T) registry.Key {
	t.Helper()
	name := fmt.Sprintf(`Software\PerfLab-PathTest-%d-%d`, os.Getpid(), time.Now().UnixNano())
	key, _, err := registry.CreateKey(registry.CURRENT_USER, name, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		key.Close()
		if err := registry.DeleteKey(registry.CURRENT_USER, name); err != nil {
			t.Error(err)
		}
	})
	return key
}

func TestWindowsAppendUserPath(t *testing.T) {
	local := filepath.Join(t.TempDir(), "Личные файлы")
	t.Setenv("LOCALAPPDATA", local)
	bin := filepath.Join(local, "PerfLab", "bin")
	const compact = `%LOCALAPPDATA%\PerfLab\bin`
	for _, tc := range []struct {
		name    string
		kind    uint32
		before  string
		after   string
		changed bool
	}{
		{"missing", registry.NONE, "", compact, true},
		{"empty", registry.SZ, "", bin, true},
		{"literal", registry.SZ, `C:\Old;%UNKNOWN%\bin;`, `C:\Old;%UNKNOWN%\bin;` + bin, true},
		{"expandable", registry.EXPAND_SZ, `%SystemRoot%\system32`, `%SystemRoot%\system32;` + compact, true},
		{"already present", registry.EXPAND_SZ, `C:\Old;"%LOCALAPPDATA%\PERFLAB\BIN\";`, `C:\Old;"%LOCALAPPDATA%\PERFLAB\BIN\";`, false},
		{"literal percent signs", registry.SZ, compact, compact + ";" + bin, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := sandboxEnvironmentKey(t)
			var err error
			switch tc.kind {
			case registry.SZ:
				err = key.SetStringValue("Path", tc.before)
			case registry.EXPAND_SZ:
				err = key.SetExpandStringValue("Path", tc.before)
			}
			if err != nil {
				t.Fatal(err)
			}
			changed, err := appendUserPath(key, bin)
			if err != nil || changed != tc.changed {
				t.Fatalf("changed %v, err %v", changed, err)
			}
			value, kind, err := key.GetStringValue("Path")
			wantKind := tc.kind
			if wantKind == registry.NONE {
				wantKind = registry.EXPAND_SZ
			}
			if err != nil || value != tc.after || kind != wantKind {
				t.Fatalf("Path = %q, type %d, err %v", value, kind, err)
			}
			changed, err = appendUserPath(key, bin)
			if err != nil || changed {
				t.Fatalf("second write: %v, %v", changed, err)
			}
		})
	}
	t.Run("unsupported type", func(t *testing.T) {
		key := sandboxEnvironmentKey(t)
		if err := key.SetDWordValue("Path", 13); err != nil {
			t.Fatal(err)
		}
		if changed, err := appendUserPath(key, bin); changed || err == nil {
			t.Fatalf("invalid type was accepted: %v, %v", changed, err)
		}
		if value, _, err := key.GetIntegerValue("Path"); value != 13 || err != nil {
			t.Fatal("existing value was changed", value, err)
		}
	})
}

type pathChildResult struct {
	Args []string
	Exe  string
	Cwd  string
	Text string
}

// The copied test executable exercises the same forwarding function as main,
// with a temporary installation root in place of the actual known folder.
func TestWindowsPathChild(t *testing.T) {
	root := os.Getenv("PERFLAB_TEST_LAUNCHER_ROOT")
	if root == "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if code, handled := forwardPathLauncher(root, exe, os.Args[1:]); handled {
		os.Exit(code)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(pathChildResult{os.Args[1:], exe, cwd, string(data)}); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(os.Stderr, "stderr")
	os.Exit(37)
}

func TestWindowsPathLauncherRoundTrip(t *testing.T) {
	key := sandboxEnvironmentKey(t)
	local := filepath.Join(t.TempDir(), "Имя; 100% &")
	t.Setenv("LOCALAPPDATA", local)
	root := filepath.Join(local, "PerfLab")
	install := filepath.Join(t.TempDir(), "Моя программа 100% & !")
	if err := os.MkdirAll(install, 0700); err != nil {
		t.Fatal(err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(testBinary)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target := filepath.Join(install, "perf-lab.exe")
	if err = writePathFile(target, source); err != nil {
		t.Fatal(err)
	}
	if changed, err := installPathLauncher(root, target, key); !changed || err != nil {
		t.Fatalf("install: %v, %v", changed, err)
	}
	if changed, err := installPathLauncher(root, target, key); changed || err != nil {
		t.Fatalf("already installed: %v, %v", changed, err)
	}
	launcher := filepath.Join(root, "bin", "perf-lab.exe")
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	launcherInfo, err := os.Stat(launcher)
	if err != nil || os.SameFile(targetInfo, launcherInfo) {
		t.Fatal("source must be independently replaceable", err)
	}
	value, _, err := key.GetStringValue("Path")
	if err != nil {
		t.Fatal(err)
	}
	searchPath, err := registry.ExpandString(value)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", searchPath)
	t.Setenv("PERFLAB_TEST_LAUNCHER_ROOT", root)
	cwd := t.TempDir()
	args := []string{"-test.run=^TestWindowsPathChild$", "--", `Д:\Каталог с пробелом\`, `кавычка " здесь`, `&|^%!()`, ""}
	probe := func(wantExe string) {
		t.Helper()
		cmd := exec.Command("perf-lab", args...)
		cmd.Dir = cwd
		cmd.Stdin = strings.NewReader("входной текст")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 37 {
			t.Fatalf("launch: %v; stderr=%s", err, &stderr)
		}
		var got pathChildResult
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err, stdout.String())
		}
		if !equalPath(got.Exe, wantExe) || !equalPath(got.Cwd, cwd) || !reflect.DeepEqual(got.Args, args) || got.Text != "входной текст" || stderr.String() != "stderr" {
			t.Fatalf("wrong process or stdio: %#v; stderr=%s", got, &stderr)
		}
	}
	probe(target)

	// Replace the real binary as the updater does. The launcher stays in
	// place and immediately executes the replacement without re-registration.
	replacement := filepath.Join(install, "next.exe")
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if err = writePathFile(replacement, source); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, target); err != nil {
		t.Fatal(err)
	}
	probe(target)

	second := filepath.Join(install, "second.exe")
	if err = os.Link(target, second); err != nil {
		t.Fatal(err)
	}
	if _, err = installPathLauncher(root, second, key); err != nil {
		t.Fatal(err)
	}
	probe(target)
	if err = os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if _, err = installPathLauncher(root, second, key); err != nil {
		t.Fatal(err)
	}
	probe(second)
	if err = os.Remove(second); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("perf-lab", args...)
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "Не удалось запустить") {
		t.Fatalf("missing target: %v, %s", err, output)
	}
	if err = os.WriteFile(filepath.Join(root, "target.txt"), []byte(launcher), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = runPathTarget(root, launcher, nil); err == nil {
		t.Fatal("launcher forwarded to itself")
	}
}
