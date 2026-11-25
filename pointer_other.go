//go:build !windows

package main

import tea "github.com/charmbracelet/bubbletea"

func viewportMouse(m tea.MouseMsg) tea.MouseMsg { return m }
