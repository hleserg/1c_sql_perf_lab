package lab

import (
	"context"
	"testing"
)

func TestHostProbeOnlyLocalEndpoint(t *testing.T) {
	for _, tc := range []struct {
		address string
		local   bool
	}{
		{".", true}, {"(local)", true}, {"localhost", true}, {"localhost:1448", true}, {"127.0.0.1:50001", true}, {"[::1]:1433", true},
		{"SQLNODE", true}, {"other:1433", false}, {"localhost.example:1433", false}, {"", false}, {"192.0.2.1", false},
	} {
		if got := isLocalSQLEndpoint(tc.address, "sqlnode"); got != tc.local {
			t.Errorf("%q: %t", tc.address, got)
		}
	}
	p := Example()
	p.SQLHost = "remote.invalid"
	for _, s := range StandHostSections(context.Background(), p) {
		if s.Error == "" || len(s.Rows) != 0 {
			t.Fatal(s)
		}
	}
}
func TestHostProbeRows(t *testing.T) {
	if rows, err := decodeHostRows([]byte(`[{"cells":["Сбалансированная","GUID"]}]`), 2); err != nil || rows[0][0] != "Сбалансированная" {
		t.Fatal(rows, err)
	}
	for _, body := range []string{"[]", `[{"cells":["off"]}]`, "access denied"} {
		if _, err := decodeHostRows([]byte(body), 2); err == nil {
			t.Fatal(body)
		}
	}
}
