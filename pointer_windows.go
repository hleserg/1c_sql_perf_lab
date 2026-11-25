package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/windows"
	"os"
)

// Win32 mouse records use screen-buffer coordinates, including scrollback.
func viewportMouse(m tea.MouseMsg) tea.MouseMsg {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(windows.Handle(os.Stdout.Fd()), &info) == nil {
		m.X -= int(info.Window.Left)
		m.Y -= int(info.Window.Top)
	}
	return m
}
