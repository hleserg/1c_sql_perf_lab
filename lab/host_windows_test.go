package lab

import (
	"context"
	"strings"
	"testing"
)

// Exercise the actual PowerShell 5 / WMI providers on the Windows CI runner.
// Defender is optional on Windows Server and a missing provider is a result.
func TestWindowsLocalHostProviders(t *testing.T) {
	for _, check := range windowsHostChecks {
		t.Run(Label(check.id), func(t *testing.T) {
			s := readHostSection(context.Background(), check)
			if check.id == 20 && strings.HasPrefix(s.Error, "Нет данных ОС: ") {
				t.Log(s.Error)
				return
			}
			if s.Error != "" || len(s.Rows) == 0 {
				t.Fatalf("%s: %s, rows=%d", s.Title, s.Error, len(s.Rows))
			}
			if check.id == 12 && !strings.Contains(s.Rows[0][1], "{") {
				t.Fatalf("missing plan GUID: %v", s.Rows)
			}
			if check.id == 20 {
				for _, state := range s.Rows[0][:2] {
					if state != "True" && state != "False" {
						t.Fatalf("unexpected Defender state %q", state)
					}
				}
			}
			if check.id == 37 {
				for _, r := range s.Rows {
					if r[0] != "" && r[1] != "0" && r[2] != "n/a" && r[3] != "n/a" {
						return
					}
				}
				t.Fatal("no running service process with performance data")
			}
		})
	}
}
