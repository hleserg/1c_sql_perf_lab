# Check the registration owned by the local cluster, plus the ERP/BSP exchange
# switches in this very session, before entering a posting transaction.
function Assert-PostingIsolation {
    $blocked = Read-ErpMember (Read-ErpMember $session SessionParameters) РаботаСВнешнимиРесурсамиЗаблокирована
    $sync = Read-ErpMember (Read-ErpMember (Read-ErpMember $session Constants) ИспользоватьСинхронизациюДанных) Get @()
    if ($blocked -isnot [bool] -or !$blocked -or $sync -isnot [bool] -or $sync) { throw 'Exchange protection is not active' }

    $agent = $connector.ConnectAgent(('tcp://127.0.0.1:' + $env:PERFLAB_AGENT_PORT))
    $matches = @()
    foreach ($cluster in $agent.GetClusters()) {
        if ([int]$cluster.MainPort -ne [int]$env:PERFLAB_MANAGER_PORT) { continue }
        $login = [string]$env:PERFLAB_CLUSTER_USER
        $pwd = [string]$env:PERFLAB_CLUSTER_PASSWORD
        $null = $agent.Authenticate($cluster, $login, $pwd)
        foreach ($w in $agent.GetWorkingProcesses($cluster)) {
            if ($w.HostName -notin @($env:COMPUTERNAME,'localhost','127.0.0.1','::1',[Net.Dns]::GetHostEntry($env:COMPUTERNAME).HostName) -or $w.MainPort -lt 1) {continue}
            $wp = $connector.ConnectWorkingProcess(('tcp://127.0.0.1:' + [int]$w.MainPort))
            $null = $wp.AuthenticateAdmin($login, $pwd)
            $null = $wp.AddAuthentication([string]$env:PERFLAB_ERP_USER,[string]$env:PERFLAB_ERP_PASSWORD)
            foreach ($ib in $wp.GetInfoBases()) {
                $sql = ([string]$ib.DBServerName).Split('\')
                $sqlHost = $sql[0]
                $instance = if ($sql.Count -eq 2) { $sql[1] } else { '' }
                if ($sql.Count -le 2 -and $sqlHost -in @('.', $env:COMPUTERNAME,'localhost','127.0.0.1','::1',[Net.Dns]::GetHostEntry($env:COMPUTERNAME).HostName) -and
                    $instance -eq $env:PERFLAB_SQL_INSTANCE -and $ib.Name -eq $env:PERFLAB_ERP_DATABASE -and $ib.DBName -eq $env:PERFLAB_SQL_DATABASE -and $ib.DBMS -eq 'MSSQLServer') {
                    $matches += $ib
                }
            }
            break
        }
    }
    if ($matches.Count -ne 1 -or $matches[0].ScheduledJobsDenied -isnot [bool] -or !$matches[0].ScheduledJobsDenied) { throw 'Scheduled jobs must be denied' }
}
