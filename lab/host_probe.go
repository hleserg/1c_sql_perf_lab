package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func readHostSection(ctx context.Context, check hostCheck) (s Section) {
	s = Section{ID: check.id, Title: check.title, Columns: check.columns}
	start := time.Now()
	defer func() { s.ElapsedMS = time.Since(start).Milliseconds() }()
	bounded, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	// Each fixed program queries local read-only providers. The JSON wrapper
	// preserves a single row as an array on Windows PowerShell 5.1.
	script := `$ErrorActionPreference='Stop'
$ProgressPreference='SilentlyContinue'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
try {
    $rows = @( & {` + check.script + `
    })
    ConvertTo-Json -Compress -Depth 3 -InputObject $rows
} catch {
    [Console]::Error.WriteLine($_.Exception.Message)
    exit 1
}`
	cmd := exec.CommandContext(bounded, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePS(script))
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	out, err := cmd.Output()
	if err != nil {
		if bounded.Err() != nil {
			err = bounded.Err()
		} else if msg := strings.TrimSpace(diagnostic.String()); msg != "" {
			err = errors.New(msg)
		}
		s.Error = "Нет данных ОС: " + err.Error()
		return
	}
	s.Rows, err = decodeHostRows(out, len(check.columns))
	if err != nil {
		s.Error = "Нет данных ОС: " + err.Error()
	}
	return
}

func decodeHostRows(b []byte, width int) ([][]string, error) {
	var records []struct{ Cells []string }
	if err := json.Unmarshal(b, &records); err != nil {
		return nil, fmt.Errorf("ответ Windows не удалось прочитать: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("локальный поставщик Windows не вернул строк")
	}
	rows := make([][]string, 0, len(records))
	for _, r := range records {
		if len(r.Cells) != width {
			return nil, errors.New("в ответе Windows отсутствуют поля проверки")
		}
		rows = append(rows, r.Cells)
	}
	return rows, nil
}
