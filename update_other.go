//go:build !windows

package main

import "errors"

func handoffUpdate(s stagedUpdate, target, root string) error {
	return errors.New("перезапуск доступен на Windows")
}
