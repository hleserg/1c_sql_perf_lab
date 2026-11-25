package lab

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func platformRoot() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX64, 0)
}

func registeredDefault(path string) (string, error) {
	key, err := registry.OpenKey(registry.CLASSES_ROOT, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer key.Close()
	value, _, err := key.GetStringValue("")
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return value, err
}

func registerConnector(ctx context.Context, dll string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("для регистрации COM открой Perf Lab от имени администратора и нажми «Исправить подключение 1С»")
	}
	cls, err := registeredDefault(`V83.COMConnector\CLSID`)
	if err != nil {
		return err
	}
	if cls != "" {
		if _, err := windows.GUIDFromString(cls); err != nil {
			return errors.New("повреждена регистрация V83.COMConnector; запусти восстановление платформы через установщик 1С")
		}
		other, err := registeredDefault(`CLSID\` + cls + `\InprocServer32`)
		if err != nil {
			return err
		}
		if other != "" && !strings.EqualFold(filepath.Clean(strings.Trim(other, "\"")), dll) {
			return fmt.Errorf("V83.COMConnector зарегистрирован для другой установки (%s). Сверь выбранную сборку с агентом 1С и настрой нужную версию через установщик", other)
		}
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, filepath.Join(system, "regsvr32.exe"), "/s", dll)
	cmd.Dir = filepath.Dir(dll)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("регистрация comcntr.dll: %w. Восстанови компоненты COM через установщик выбранной сборки 1С", err)
	}
	return nil
}

// Resolve the 64-bit system host independently of PATH and the launch directory.
func platformPowerShell(ctx context.Context, script string) (*exec.Cmd, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePS(script)), nil
}
