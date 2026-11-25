package lab

import (
	"context"
	"testing"
)

func TestWindowsLocalRASInventory(t *testing.T) {
	var procs []platformProcess
	if err := localAdminJSON(context.Background(), rasHostScript, &procs); err != nil {
		t.Fatal(err)
	}
}

// Exercise the PowerShell/Go boundary in Windows CI using a local COM stand-in.
func TestWindowsCOMDiscoveryAdaptor(t *testing.T) {
	const fake = `
function New-Object {
 param([string]$ComObject)
 $db=[pscustomobject]@{Name='Стенд';DBName='fixture';DBServerName='node\instance';DBMS='MSSQLServer'}
 $wp=New-Module -AsCustomObject -ArgumentList $db -ScriptBlock {param($db)
 function AuthenticateAdmin($u,$p){}; function AddAuthentication($u,$p){}; function GetInfoBases(){,$db};Export-ModuleMember -Function *}
 $agent=New-Module -AsCustomObject -ScriptBlock {
 function GetClusters(){[pscustomobject]@{HostName='localhost';MainPort=2541}}
 function Authenticate($c,$u,$p){};function GetWorkingProcesses($c){[pscustomobject]@{HostName='localhost';MainPort=2562}};Export-ModuleMember -Function *}
 New-Module -AsCustomObject -ArgumentList $agent,$wp -ScriptBlock {param($agent,$wp)
 function ConnectAgent($target){if($target -ne 'tcp://127.0.0.1:2540'){throw 'wrong endpoint'};return $agent}
 function ConnectWorkingProcess($target){if($target -ne 'tcp://127.0.0.1:2562'){throw 'wrong worker'};return $wp};Export-ModuleMember -Function *}
}
`
	var rows []agentRegistration
	if err := localAdminJSON(context.Background(), fake+erpAgentScript, &rows, "PERFLAB_AGENT_PORT=2540", "PERFLAB_MANAGER_PORT=2541"); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "Стенд" || rows[0].DB != "fixture" || rows[0].Port != 2541 {
		t.Fatal(rows)
	}
}
