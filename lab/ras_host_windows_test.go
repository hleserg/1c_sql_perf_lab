package lab

import (
	"context"
	"strings"
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

func TestWindowsCOMReportsActualWorkloadStage(t *testing.T) {
	const fake = `
function New-Object {
 param([string]$ComObject)
 if($env:FIXTURE_STAGE -eq 'activate'){throw 'class not registered secret'}
 New-Module -AsCustomObject -ScriptBlock {
 function Connect($s){
  if($env:FIXTURE_STAGE -eq 'connect'){throw 'authentication failed secret'}
  New-Module -AsCustomObject -ScriptBlock {
   function NewObject($name){
    New-Module -AsCustomObject -ScriptBlock {
     $Text='';function Execute(){
      if($env:FIXTURE_STAGE -eq 'query'){throw 'table not found secret'}
      New-Module -AsCustomObject -ScriptBlock {
       function Select(){
        New-Module -AsCustomObject -ScriptBlock {
         function Next(){return $env:FIXTURE_STAGE -ne 'empty'}
         Export-ModuleMember -Function *
        }
       };Export-ModuleMember -Function *
      }
     };Export-ModuleMember -Variable Text -Function *
    }
   };Export-ModuleMember -Function *
  }
 };Export-ModuleMember -Function *
 }
}
`
	for _, s := range []string{"activate", "connect", "query", "uuid", "empty"} {
		t.Run(s, func(t *testing.T) {
			t.Setenv("FIXTURE_STAGE", s)
			cmd, err := platformPowerShell(context.Background(), fake+erpCOMScript+"\n"+discoverDocumentPS)
			if err != nil {
				t.Fatal(err)
			}
			var stderr racOutput
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if s == "empty" {
				if err != nil || len(out) != 0 {
					t.Fatalf("%v %q %s", err, out, stderr.data.String())
				}
				return
			}
			got := stderr.data.String()
			if err == nil || !strings.HasPrefix(got, "PERFLAB-COM|"+s+"|") || strings.Contains(got, "secret") {
				t.Fatalf("%v %q", err, got)
			}
		})
	}
}
