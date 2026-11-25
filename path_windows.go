package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/gofrs/flock"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func pathLauncherRoot() (string, error) {
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(local, "PerfLab"), nil
}

func equalPath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func pathLauncherExitCode() (int, bool) {
	exe, err := os.Executable()
	if err != nil {
		return 0, false
	}
	root, err := pathLauncherRoot()
	if err != nil {
		return 0, false
	}
	return forwardPathLauncher(root, exe, os.Args[1:])
}

func forwardPathLauncher(root, exe string, args []string) (int, bool) {
	if !equalPath(exe, filepath.Join(root, "bin", "perf-lab.exe")) {
		return 0, false
	}
	code, err := runPathTarget(root, exe, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Не удалось запустить perf-lab из PATH:", err)
		return 1, true
	}
	return code, true
}

func runPathTarget(root, exe string, args []string) (int, error) {
	data, err := os.ReadFile(filepath.Join(root, "target.txt"))
	if err != nil {
		return 0, err
	}
	target := string(data)
	if !filepath.IsAbs(target) {
		return 0, errors.New("не задан полный путь к EXE; открой исходную программу")
	}
	info, err := os.Stat(target)
	if err != nil {
		return 0, err
	}
	self, err := os.Stat(exe)
	if err != nil {
		return 0, err
	}
	if os.SameFile(self, info) {
		return 0, errors.New("путь указывает на сам файл запуска; открой исходную программу")
	}

	// Both processes share the console. Let the application receive Ctrl+C and
	// finish its cleanup before the calling shell receives the exit status.
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	cmd := exec.Command(target, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err = cmd.Run(); err != nil {
		var status *exec.ExitError
		if errors.As(err, &status) {
			return status.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}

func ensureUserPath(exe string) error {
	root, err := pathLauncherRoot()
	if err != nil {
		return err
	}
	env, _, err := registry.CreateKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer env.Close()
	changed, err := installPathLauncher(root, exe, env)
	if changed {
		go broadcastPathChange()
	}
	return err
}

func installPathLauncher(root, exe string, env registry.Key) (bool, error) {
	bin := filepath.Join(root, "bin")
	launcher := filepath.Join(bin, "perf-lab.exe")
	if !filepath.IsAbs(exe) || equalPath(launcher, exe) {
		return false, errors.New("для регистрации PATH открой исходный EXE")
	}
	if err := os.MkdirAll(bin, 0700); err != nil {
		return false, err
	}
	lock := flock.New(filepath.Join(root, "path.lock"))
	if err := lock.Lock(); err != nil {
		return false, err
	}
	defer lock.Unlock()
	launcherInfo, err := os.Stat(launcher)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if launcherInfo != nil && !launcherInfo.Mode().IsRegular() {
		return false, fmt.Errorf("%s должен быть обычным файлом", launcher)
	}

	// Keep the first installation active when someone opens a second copy.
	pointer := filepath.Join(root, "target.txt")
	data, err := os.ReadFile(pointer)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	active := string(data)
	if active != "" && !equalPath(active, exe) && filepath.IsAbs(active) && !equalPath(active, launcher) {
		info, err := os.Stat(active)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		if err == nil && info.Mode().IsRegular() && !os.SameFile(info, launcherInfo) {
			exe = active
		}
	}
	if !equalPath(active, exe) {
		if err := writePathFile(pointer, strings.NewReader(exe)); err != nil {
			return false, err
		}
	}
	if launcherInfo == nil {
		// A separate image keeps the original free for in-place updates. The
		// launcher forwards each invocation to the live target, even after updates.
		source, err := os.Open(exe)
		if err != nil {
			return false, err
		}
		defer source.Close()
		if err = writePathFile(launcher, source); err != nil {
			return false, err
		}
	}
	return appendUserPath(env, bin)
}

func writePathFile(name string, r io.Reader) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".perf-lab-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), name)
}

func appendUserPath(env registry.Key, bin string) (bool, error) {
	value, kind, err := env.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		kind = registry.EXPAND_SZ
	} else if err != nil {
		return false, err
	}
	for _, entry := range filepath.SplitList(value) {
		entry = strings.TrimSpace(entry)
		if kind == registry.EXPAND_SZ {
			entry, err = registry.ExpandString(entry)
			if err != nil {
				return false, err
			}
		}
		if entry != "" && equalPath(entry, bin) {
			return false, nil
		}
	}
	entry := bin
	if kind == registry.EXPAND_SZ {
		const local = `%LOCALAPPDATA%\PerfLab\bin`
		if expanded, err := registry.ExpandString(local); err == nil && equalPath(expanded, bin) {
			entry = local
		}
	}
	if strings.Contains(bin, ";") {
		entry = `"` + entry + `"`
	}
	if value != "" && !strings.HasSuffix(value, ";") {
		value += ";"
	}
	value += entry
	if kind == registry.EXPAND_SZ {
		err = env.SetExpandStringValue("Path", value)
	} else {
		err = env.SetStringValue("Path", value)
	}
	return err == nil, err
}

func broadcastPathChange() {
	// WM_SETTINGCHANGE, sent to top-level windows so Explorer can refresh
	// the user environment it passes to newly launched programs.
	message, _ := windows.UTF16PtrFromString("Environment")
	send := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	send.Call(0xffff, 0x001a, 0, uintptr(unsafe.Pointer(message)), 0x0002, 250, 0)
}
