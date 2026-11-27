package lab

import (
	"context"
	"strings"
	"testing"
)

func TestPostingRejectsUnprotectedExchange(t *testing.T) {
	const harness = `
function Read-ErpMember($obj,$name,$arguments) {
	if ($name -eq 'РаботаСВнешнимиРесурсамиЗаблокирована') {return ($env:BLOCKED -eq '1')}
	if ($name -eq 'Get') {return ($env:SYNC -eq '1')}
	return 'mock'
}
try { Assert-PostingIsolation; exit 2 } catch { [Console]::WriteLine($_.Exception.GetBaseException().Message) }
`
	for _, flags := range [][2]string{{"0", "0"}, {"1", "1"}} {
		t.Setenv("BLOCKED", flags[0])
		t.Setenv("SYNC", flags[1])
		cmd, err := platformPowerShell(context.Background(), postGuardPS+"\n"+harness)
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.Output()
		if err != nil || !strings.Contains(string(out), "Exchange protection is not active") {
			t.Fatalf("%v %s", err, out)
		}
	}
}
