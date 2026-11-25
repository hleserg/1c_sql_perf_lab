$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
# Read only the platform processes and listening ports. Never export command lines.
$items = @(Get-CimInstance Win32_Process -Filter "Name='ras.exe' OR Name='ragent.exe'" | ForEach-Object {
    $ports = @()
    if ($_.Name -eq 'ras.exe') {
        $ports = @(Get-NetTCPConnection -OwningProcess $_.ProcessId -State Listen -ErrorAction SilentlyContinue | Select-Object -ExpandProperty LocalPort -Unique)
    } else {
        if ($_.CommandLine -match '(?:^|\s)(?:-|/)port(?:\s+|=)"?(\d+)(?:"|\s|$)') { $ports = @([int]$Matches[1]) }
        elseif ($_.CommandLine) { $ports = @(1540) }
    }
    [pscustomobject]@{ Name=$_.Name; Path=$_.ExecutablePath; Ports=@($ports) }
})
[Console]::WriteLine((ConvertTo-Json -InputObject $items -Compress -Depth 3))
