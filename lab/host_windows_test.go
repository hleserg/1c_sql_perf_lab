package lab

import (
	"context"
	"strings"
	"testing"
)

func TestPowerPlanWhenWMIProviderIsUnavailable(t *testing.T) {
	check := windowsHostChecks[0]
	check.script = `function Get-CimInstance {
    param($ClassName,$Namespace,$Filter,$Property)
    if ($ClassName -eq 'Win32_PowerPlan') { throw 'provider unavailable' }
    [pscustomobject]@{ DeviceID='CPU_TEST'; CurrentClockSpeed=1234 }
}
` + check.script
	result := readHostSection(context.Background(), check)
	if result.Error != "" || len(result.Rows) != 1 {
		t.Fatalf("powercfg fallback: %s %#v", result.Error, result.Rows)
	}
	row := result.Rows[0]
	if !strings.HasPrefix(row[1], `Microsoft:PowerPlan\{`) || !strings.HasSuffix(row[1], "}") || row[2] != "CPU_TEST 1234" {
		t.Fatalf("unexpected power scheme: %q", row)
	}
}

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
