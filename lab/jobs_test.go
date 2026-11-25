package lab

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestSQLJobsWithoutERP(t *testing.T) {
	for _, j := range Jobs {
		t.Run(j.Key, func(t *testing.T) {
			r, _ := testRun(t)
			r.JobKey = j.Key
			rep, path, e := r.Execute(context.Background(), nil)
			if e != nil {
				t.Fatal(e)
			}
			if len(rep.Sections) == 0 || rep.Job != j.Name {
				t.Fatal(rep)
			}
			if j.Key == "stats-erp" && len(rep.Phases) != 2 || j.Key != "stats-erp" && len(rep.Phases) != 0 {
				t.Fatal(rep.Phases)
			}
			b, e := os.ReadFile(strings.TrimSuffix(path, ".json") + ".html")
			if e != nil || !strings.Contains(string(b), j.Name) {
				t.Fatal(e)
			}
		})
	}
}
