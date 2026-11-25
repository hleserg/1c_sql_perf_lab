//go:build !windows

package lab

import "errors"

func decodeRACConsole(raw []byte) (string, error) {
	return "", errors.New("неизвестная кодировка rac")
}
