package main

import (
	"os"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
)

func runTerminal(model tea.Model) error {
	if runtime.GOOS != "windows" {
		_, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
		return err
	}
	// Classic conhost reports changes to its scrollback buffer through the input queue.
	// Layout follows the visible window, including height-only changes to that window.
	viewport := func() (tea.WindowSizeMsg, bool) {
		w, h, err := term.GetSize(os.Stdout.Fd())
		return tea.WindowSizeMsg{Width: w, Height: h}, err == nil && w > 0 && h > 0
	}
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if mouse, ok := msg.(tea.MouseMsg); ok {
			return viewportMouse(mouse)
		}
		if _, resize := msg.(tea.WindowSizeMsg); resize {
			if size, ok := viewport(); ok {
				return size
			}
		}
		return msg
	}))
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(350 * time.Millisecond)
		defer ticker.Stop()
		var last tea.WindowSizeMsg
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if size, ok := viewport(); ok && size != last {
					last = size
					p.Send(size)
				}
			}
		}
	}()
	_, err := p.Run()
	return err
}
