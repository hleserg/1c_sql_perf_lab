$ErrorActionPreference = 'Stop'
$dir = $env:PERFLAB_UPDATE_DIR
$target = $env:PERFLAB_UPDATE_TARGET
$next = Join-Path $dir 'perf-lab.exe'
$previous = Join-Path $dir 'previous.exe'
$ready = $false
$swapped = $false
$cleanup = $false
$lock = $null

function Start-PerfLab([string]$message) {
    $env:PERFLAB_UPDATE_NOTICE = $message
    $start = New-Object System.Diagnostics.ProcessStartInfo
    $start.FileName = $target
    $start.Arguments = $env:PERFLAB_UPDATE_ARGS
    $start.WorkingDirectory = [IO.Path]::GetDirectoryName($target)
    $start.UseShellExecute = $false
    # The helper has no console. Windows creates a visible console for the application.
    $start.CreateNoWindow = $false
    $process = [Diagnostics.Process]::Start($start)
    $process.Dispose()
}

try {
    $lock = [IO.FileStream]::new("$target.update.lock", [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None, 4096, [IO.FileOptions]::DeleteOnClose)
    if ((Get-FileHash -LiteralPath $next -Algorithm SHA256).Hash -ne $env:PERFLAB_UPDATE_DIGEST) {
        throw 'Контрольная сумма подготовленного EXE изменилась.'
    }
    $old = [Diagnostics.Process]::GetProcessById([int]$env:PERFLAB_UPDATE_PARENT)
    [void]$old.Handle
    [IO.File]::WriteAllText((Join-Path $dir 'ready'), '1')
    $ready = $true
    if (-not $old.WaitForExit(60000)) { throw 'Прежний процесс не завершился.' }
    $until = [DateTime]::UtcNow.AddSeconds(12)
    while ($true) {
        try {
            [IO.File]::Replace($next, $target, $previous)
            $swapped = $true
            break
        } catch [IO.IOException] {
            if ([DateTime]::UtcNow -ge $until) { throw }
            Start-Sleep -Milliseconds 250
        }
    }
    Start-PerfLab "Обновлено до $env:PERFLAB_UPDATE_TAG"
    $cleanup = $true
} catch {
    $detail = $_.Exception.Message
    if (-not $ready) {
        [IO.File]::WriteAllText((Join-Path $dir 'error.txt'), $detail)
        exit 1
    }
    try {
        if ($swapped) { [IO.File]::Replace($previous, $target, (Join-Path $dir 'failed.exe')) }
        if (-not $old.HasExited) { throw 'Прежнее окно ещё открыто.' }
        Start-PerfLab "Обновление не установлено: $detail"
        $cleanup = $true
    } catch {
        Add-Type -AssemblyName System.Windows.Forms
        [void][Windows.Forms.MessageBox]::Show("Перезапуск не завершён. $detail`n$($_.Exception.Message)`nПрограмма: $target`nФайлы обновления: $dir", 'SQL Perf Lab')
    }
} finally {
    if ($lock) { $lock.Dispose() }
    if ($cleanup) { Remove-Item -LiteralPath $dir -Recurse -Force -ErrorAction SilentlyContinue }
}
