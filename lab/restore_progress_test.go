package lab

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestRestoreSamplesRemainIndependent(t *testing.T) {
	var updates []RestoreProgress
	l := &restoreLog{emit: func(_ string, p RestoreProgress) { updates = append(updates, p) }}
	l.stage(RestoreVerify, "verify")
	l.sample(0)
	l.sample(37)
	l.p.Verify.Complete = true
	l.stage(RestoreWrite, "write")
	l.sample(14)
	if updates[0].Verify.Percent != nil || *updates[1].Verify.Percent != 0 || *updates[2].Verify.Percent != 37 || updates[2].Verify.Complete || updates[3].Write.Percent != nil || *updates[4].Write.Percent != 14 || *updates[4].Verify.Percent != 37 {
		t.Fatal(updates)
	}
}

func TestRestoreCompletionIsPersistedWithoutInventingSamples(t *testing.T) {
	r := Run{Root: t.TempDir(), Demo: true, Restore: &RestorePlan{Demo: true, Target: "demo_copy"}}
	rep, path, e := r.Execute(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	var saved Report
	content, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(content, &saved); e != nil {
		t.Fatal(e)
	}
	for _, p := range []*RestoreProgress{rep.Restore, saved.Restore} {
		if p == nil || p.Stage != RestoreReady || p.State != "ONLINE / MULTI_USER" || !p.Verify.Complete || !p.Write.Complete || p.Verify.Percent != nil || p.Write.Percent != nil {
			t.Fatal(p)
		}
	}
}
