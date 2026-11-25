package lab

import (
	"reflect"
	"slices"
	"testing"
)

func TestInspectionIDsCoverEveryQueryAndTimedSampleOnce(t *testing.T) {
	ids := InspectionIDs()
	if !slices.IsSorted(ids) || !slices.Contains(ids, 10) {
		t.Fatalf("missing timed sample or inconsistent catalog order: %v", ids)
	}
	for i, id := range ids {
		if ByID(id).ID != id || (i > 0 && ids[i-1] == id) {
			t.Fatalf("unknown or repeated item %d", id)
		}
	}
	for _, c := range inspections {
		if !slices.Contains(ids, c.id) {
			t.Errorf("missing item %d for %q", c.id, c.title)
		}
	}
}

func TestFileIODeltasPairFilesWithinDatabase(t *testing.T) {
	before := [][]string{
		{"2", "1", "tempdb", "data", "1", "10", "2", "50"},
		{"5", "1", "test", "data", "100", "60", "1000", "200"},
		{"5", "2", "test", "log", "100", "60", "1000", "200"},
	}
	after := [][]string{
		{"5", "1", "test", "data", "100", "62", "1000", "209"},
		{"2", "1", "tempdb", "data", "2", "10", "5", "50"},
		{"5", "2", "test", "log", "0", "1", "0", "1"},
		{"5", "3", "test", "new", "5", "5", "5", "5"},
	}
	want := [][]string{
		{"test", "data", "0", "2", "—", "4.50", "5.0"},
		{"tempdb", "data", "1", "0", "3.00", "—", "5.0"},
	}
	if got := fileIODeltas(before, after, 5000); !reflect.DeepEqual(got, want) {
		t.Fatalf("file I/O pairing: got %v, want %v", got, want)
	}
}

// testInspectionEvidence verifies the result of the actual inspect job on the
// disposable SQL instance. Empty Agent/restore histories are valid for a fresh server.
func testInspectionEvidence(t *testing.T, sections []Section, database string) {
	t.Helper()
	seen := map[int]bool{}
	var diskDatabases, ioDatabases []string
	var hasInstanceSetting, hasScopedSetting, hasSQLVersion bool
	for _, s := range sections {
		seen[s.ID] = true
		if s.Error != "" || s.Baseline != (s.ID == 35 || s.ID == 36) {
			t.Errorf("unexpected diagnostic or baseline flag for %q: %s, %v", s.Title, s.Error, s.Baseline)
		}
		for _, r := range s.Rows {
			if len(r) != len(s.Columns) || len(r) == 0 {
				t.Fatalf("wrong row for %q: %v", s.Title, r)
			}
			switch s.ID {
			case 2:
				hasSQLVersion = r[0] != "" && r[0] != "—"
			case 10:
				ioDatabases = append(ioDatabases, r[0])
			case 26:
				if slices.Contains(s.Columns, "Прирост") {
					diskDatabases = append(diskDatabases, r[0])
				}
			case 35:
				hasInstanceSetting = hasInstanceSetting || r[0] == "max server memory (MB)"
				hasScopedSetting = hasScopedSetting || r[0] == "MAXDOP"
			}
		}
	}
	for _, id := range InspectionIDs() {
		if !seen[id] {
			t.Errorf("missing evidence for catalog item %d", id)
		}
	}
	for _, db := range []string{"tempdb", database} {
		if !slices.Contains(ioDatabases, db) || !slices.Contains(diskDatabases, db) {
			t.Errorf("missing I/O or file growth evidence for %s: %v, %v", db, ioDatabases, diskDatabases)
		}
	}
	if !hasSQLVersion || !hasInstanceSetting || !hasScopedSetting {
		t.Error("missing build or configuration values from the live instance")
	}
}
