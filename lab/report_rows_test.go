package lab

import (
	"math"
	"testing"
	"time"
)

func TestRowsShowOnlyObservedDurations(t *testing.T) {
	r := Report{Samples: 2, Phases: []Phase{
		{Name: "baseline", Source: "1С", Complete: true, Raw: []map[string]float64{
			{"A": 10, "B": 20, "C": 50}, {"A": 30, "B": 20, "C": 50},
		}},
		{Name: "trial", Baseline: "baseline", Source: "1С", Complete: true, Raw: []map[string]float64{
			{"A": 12, "C": 60, "invalid": math.NaN()}, {"B": 30, "C": 40},
		}},
	}}
	rows := Rows(r)
	if len(rows) != 6 {
		t.Fatalf("expected all three observed operations in each phase, got %+v", rows)
	}
	for i, want := range []Row{{Name: "A", Median: 12, N: 1}, {Name: "B", Median: 30, N: 1}, {Name: "C", Median: 50, N: 2, Compared: true}} {
		got := rows[i+3]
		if got.Name != want.Name || got.Median != want.Median || got.N != want.N || got.Compared != want.Compared {
			t.Errorf("trial row %d: %+v, want %+v", i, got, want)
		}
	}
}

func TestRowsRejectPartialSeriesAndDifferentWorkloads(t *testing.T) {
	samples := []map[string]float64{{"time": 12}, {"time": 18}}
	r := Report{Samples: 2, Phases: []Phase{
		{Name: "base", Source: "1С", Complete: true, Raw: samples},
		{Name: "short", Source: "1С", Baseline: "base", Complete: true, Raw: samples[:1]},
		{Name: "unfinished", Source: "1С", Baseline: "base", Raw: samples},
		{Name: "sql", Source: "SQL", Baseline: "base", Complete: true, Raw: samples},
		{Name: "using short base", Source: "1С", Baseline: "short", Complete: true, Raw: samples},
	}}
	for _, row := range Rows(r) {
		if row.Compared {
			t.Errorf("unexpected comparison: %+v", row)
		}
	}
}

func TestRowsSelectedRunUsesFirstCompletePhase(t *testing.T) {
	raw := []map[string]float64{{"time": 10}, {"time": 20}}
	r := Report{Samples: 2, Phases: []Phase{
		{Name: "base", Complete: true, Raw: raw},
		{Name: "trial", Complete: true, Raw: raw},
		{Name: "stopped", Raw: raw[:1]},
	}}
	rows := Rows(r)
	if rows[0].Compared || !rows[1].Compared || rows[1].BaselineMedian != 15 || rows[1].Delta != 0 || rows[2].Compared || rows[2].BaselineMedian != 0 {
		t.Fatalf("selected-run comparison: %+v", rows)
	}
	r.Phases = r.Phases[:2]
	r.Phases[0].Complete, r.Phases[1].Complete = false, false
	r.Finished = time.Now()
	if !Rows(r)[1].Compared {
		t.Fatal("finished legacy report with all expected samples lost its comparison")
	}
	r.Error = "cancelled"
	if Rows(r)[1].Compared {
		t.Fatal("incomplete legacy report claimed a comparison")
	}
}
