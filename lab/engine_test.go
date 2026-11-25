package lab

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testRun(t *testing.T) (Run, *Demo) {
	t.Helper()
	p := Example()
	p.AsyncStats = true
	p.Cluster.Connections = 128
	d := NewDemo()
	d.Delay = 0
	return Run{Root: t.TempDir(), Profile: p, Demo: true, Backend: d}, d
}
func TestIndependentCombinedAndFinalControl(t *testing.T) {
	r, d := testRun(t)
	rep, file, e := r.Execute(context.Background(), []int{19, 24, 25, ClusterID})
	if e != nil {
		t.Fatal(e)
	}
	if len(rep.Phases) != 7 || rep.Phases[5].Name != "Все выбранные вместе" {
		t.Fatal(rep.Phases)
	}
	for _, p := range rep.Phases {
		if len(p.Raw) != 5 || !p.Complete {
			t.Fatal(p)
		}
	}
	if d.Settings[19] != 0 || d.Settings[24] != 0 || d.Settings[25] != 0 || d.Settings[ClusterID] != 256 {
		t.Fatal(d.Settings)
	}
	if _, e = os.Stat(filepath.Join(filepath.Dir(file), "report.html")); e != nil {
		t.Fatal(e)
	}
}

type broken struct {
	*Demo
	calls int
}

func (b *broken) Measure(c context.Context) (map[string]float64, error) {
	b.calls++
	if b.Settings[19] == 1 {
		return nil, errors.New("ошибка нагрузки")
	}
	return b.Demo.Measure(c)
}
func TestFailureRestores(t *testing.T) {
	r, d := testRun(t)
	r.Backend = &broken{Demo: d}
	rep, _, e := r.Execute(context.Background(), []int{19})
	if e == nil || rep.Error == "" || d.Settings[19] != 0 {
		t.Fatal(rep, e)
	}
}
func TestRecoveryRefusesThirdValue(t *testing.T) {
	r, d := testRun(t)
	p := r.pending()
	j := Journal{Identity: "demo", Cluster: clusterKey(r.Profile), Entries: []Entry{{19, 0, 1}}}
	if e := SaveJSON(p, j); e != nil {
		t.Fatal(e)
	}
	d.Settings[19] = 4
	if e := r.Recover(context.Background()); e == nil || d.Settings[19] != 4 {
		t.Fatal(e)
	}
	d.Settings[19] = 1
	if e := r.Recover(context.Background()); e != nil || d.Settings[19] != 0 {
		t.Fatal(e)
	}
	if _, e := os.Stat(p); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}
func TestAlreadyAppliedAndBaselineOnly(t *testing.T) {
	r, d := testRun(t)
	d.Settings[19] = 1
	rep, _, e := r.Execute(context.Background(), []int{19})
	if e != nil || len(rep.Phases) != 1 || len(rep.Journal) != 1 {
		t.Fatal(rep, e)
	}
}
func TestCatalogCoverage(t *testing.T) {
	for i, it := range Catalog {
		if it.ID != i+1 {
			t.Fatal(it)
		}
	}
	if len(Catalog) != 38 {
		t.Fatal(len(Catalog))
	}
}
