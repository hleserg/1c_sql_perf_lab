package lab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedHTMLIncludesSourceAndPairedMedians(t *testing.T) {
	r := Report{Workload: "workload fixture", Samples: 1, Phases: []Phase{
		{Name: "base", Source: "worker", Complete: true, Raw: []map[string]float64{{"read": 123.456}}},
		{Name: "trial", Source: "worker", Baseline: "base", Complete: true, Raw: []map[string]float64{{"read": 97.8}}},
	}}
	path := filepath.Join(t.TempDir(), "index.html")
	if err := WriteHTML(path, r); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"workload fixture", "До, мс", "<td>123.46</td><td>97.80</td>"} {
		if !strings.Contains(string(data), expected) {
			t.Fatal(expected)
		}
	}
}
