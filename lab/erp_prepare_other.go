//go:build !windows

package lab

import (
	"context"
	"errors"
	"os/exec"
)

func platformRoot() (string, error) {
	return "", errors.New("автонастройка подключения 1С доступна на Windows")
}
func registerConnector(context.Context, string) error {
	return errors.New("регистрация COM доступна на Windows")
}

func platformPowerShell(ctx context.Context, script string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePS(script)), nil
}
