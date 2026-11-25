$ErrorActionPreference = 'Stop'
$dir = $env:PERFLAB_UPDATE_DIR
$target = $env:PERFLAB_UPDATE_TARGET
$next = [IO.Path]::Combine($dir, 'perf-lab.exe')
$previous = [IO.Path]::Combine($dir, 'previous.exe')
$ready = $false
$swapped = $false
$cleanup = $false
$lock = $null

function Start-PerfLab([string]$message) {
    $env:PERFLAB_UPDATE_NOTICE = $message
    $start = [Diagnostics.ProcessStartInfo]::new()
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
    $hash = [Security.Cryptography.SHA256]::Create()
    try {
        $stream = [IO.File]::OpenRead($next)
        try { $digest = [BitConverter]::ToString($hash.ComputeHash($stream)).Replace('-', '') }
        finally { $stream.Dispose() }
    } finally { $hash.Dispose() }
    if ($digest -ne $env:PERFLAB_UPDATE_DIGEST) {
        throw 'Контрольная сумма подготовленного EXE изменилась.'
    }
    $old = [Diagnostics.Process]::GetProcessById([int]$env:PERFLAB_UPDATE_PARENT)
    [void]$old.Handle
    [IO.File]::WriteAllText([IO.Path]::Combine($dir, 'ready'), '1')
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
            [Threading.Thread]::Sleep(250)
        }
    }
    Start-PerfLab "Обновлено до $env:PERFLAB_UPDATE_TAG"
    $cleanup = $true
} catch {
    $detail = $_.Exception.Message
    if (-not $ready) {
        [IO.File]::WriteAllText([IO.Path]::Combine($dir, 'error.txt'), $detail)
        exit 1
    }
    try {
        if ($swapped) { [IO.File]::Replace($previous, $target, [IO.Path]::Combine($dir, 'failed.exe')) }
        if (-not $old.HasExited) { throw 'Прежнее окно ещё открыто.' }
        Start-PerfLab "Обновление не установлено: $detail"
        $cleanup = $true
    } catch {
        [void][Reflection.Assembly]::LoadWithPartialName("System.Windows.Forms")
        [void][Windows.Forms.MessageBox]::Show("Перезапуск не завершён. $detail`n$($_.Exception.Message)`nПрограмма: $target`nФайлы обновления: $dir", 'SQL Perf Lab')
    }
} finally {
    if ($lock) { $lock.Dispose() }
    if ($cleanup) { try { [IO.Directory]::Delete($dir, $true) } catch [IO.IOException] {} }
}
