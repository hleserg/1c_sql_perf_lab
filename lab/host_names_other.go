//go:build !windows

package lab

import "errors"

func windowsHostNames() (string, string, error) {
	return "", "", errors.New("сбор доступен при запуске на Windows-сервере SQL")
}
