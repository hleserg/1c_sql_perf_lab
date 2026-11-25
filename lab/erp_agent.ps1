$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
# Read registration details through the local server agent and a local worker.
# A short registration only contains a display name, so it cannot identify the SQL copy.
function Local-HostName([string]$hostName) {
    $known = @('localhost','127.0.0.1','::1',$env:COMPUTERNAME,$env:PERFLAB_FQDN,$env:PERFLAB_SQL_NODE)
    return ($hostName -and ($known -contains $hostName.TrimEnd('.')))
}
$connector = New-Object -ComObject V83.COMConnector
$agent = $connector.ConnectAgent(('tcp://127.0.0.1:' + $env:PERFLAB_AGENT_PORT))
$result = @()
foreach ($cluster in $agent.GetClusters()) {
    if (!(Local-HostName $cluster.HostName)) { continue }
    $port = [int]$cluster.MainPort
    if ($env:PERFLAB_MANAGER_PORT -and $port -ne [int]$env:PERFLAB_MANAGER_PORT) { continue }
    $login = [string]$env:PERFLAB_CLUSTER_USER
    $password = [string]$env:PERFLAB_CLUSTER_PASSWORD
    $agent.Authenticate($cluster, $login, $password)
    $workers = @($agent.GetWorkingProcesses($cluster) | Where-Object { $_.MainPort -gt 0 -and (Local-HostName $_.HostName) })
    $read = $false
    $lastError = $null
    foreach ($worker in $workers) {
        try {
            $conn = $connector.ConnectWorkingProcess(('tcp://127.0.0.1:' + [int]$worker.MainPort))
            $conn.AuthenticateAdmin($login, $password)
            $conn.AddAuthentication([string]$env:PERFLAB_ERP_USER,[string]$env:PERFLAB_ERP_PASSWORD)
            $entries = @($conn.GetInfoBases())
            $batch = @()
            foreach ($ib in $entries) {
                $batch += [pscustomobject]@{Name=[string]$ib.Name; DB=[string]$ib.DBName; SQL=[string]$ib.DBServerName; DBMS=[string]$ib.DBMS; Port=$port}
            }
            $result += $batch
            $read = $true
            break
        } catch { $lastError = $_ }
    }
    if (!$read) {
        if ($lastError) { throw $lastError }
        throw 'No local 1C worker is available'
    }
}
[Console]::WriteLine((ConvertTo-Json -InputObject @($result) -Depth 3 -Compress))
