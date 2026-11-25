$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
function Quote-1C([string]$Value) { return '"' + $Value.Replace('"', '""') + '"' }
try {
    $connector = New-Object -ComObject V83.COMConnector
    $connection = 'Srvr=' + (Quote-1C $env:PERFLAB_ERP_SERVER) + ';Ref=' + (Quote-1C $env:PERFLAB_ERP_DATABASE) + ';'
    if ($env:PERFLAB_ERP_USER) {
        $connection += 'Usr=' + (Quote-1C $env:PERFLAB_ERP_USER) + ';Pwd=' + (Quote-1C $env:PERFLAB_ERP_PASSWORD) + ';'
    }
    $session = $connector.Connect($connection)
    $manager = $session.Documents.($env:PERFLAB_DOCUMENT)
    $id = $session.NewObject('UUID', $env:PERFLAB_UUID)
    $ref = $manager.GetRef($id)
    $timer = [Diagnostics.Stopwatch]::StartNew()
    $obj = $ref.GetObject()
    $timer.Stop()
    if ($null -eq $obj) { throw 'Документ по UUID не найден' }
    $data = @{}
    $data[('Чтение документа · ' + $env:PERFLAB_DOCUMENT)] = $timer.Elapsed.TotalMilliseconds
    [Console]::WriteLine(($data | ConvertTo-Json -Compress))
} catch {
    [Console]::Error.WriteLine($_.Exception.Message)
    exit 1
}
