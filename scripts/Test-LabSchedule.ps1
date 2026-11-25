# Exercise the real planner without registering or launching any task.
$ErrorActionPreference = 'Stop'
$dir = Join-Path ([IO.Path]::GetTempPath()) ("perf lab " + [Guid]::NewGuid().ToString('N'))
$install = Join-Path $PSScriptRoot 'Install-LabSchedule.ps1'
try {
    $null = New-Item -ItemType Directory -Force -Path (Join-Path $dir 'local')
    $exe = (New-Item -ItemType File -Path (Join-Path $dir 'fake lab.exe')).FullName
    @{ExpectedServer='TEST-NODE'; Database='measurement'} | ConvertTo-Json | Set-Content -Encoding UTF8 -LiteralPath (Join-Path $dir 'local/stand.json')
    $base = @{ Executable=$exe; LabHome=$dir; Preview=$true }
    $settings = @{ ExpectedServer='TEST-NODE'; MeasurementDatabase='measurement'; BackupSource='fixture'; BackupFolder='D:\fresh copies\' }
    $weekly = & $install @base @settings -Mode WeeklyVerify
    if ($weekly.Execute -ne $exe -or $weekly.Arguments -notlike '*"weekly-verify"*' -or
        -not $weekly.Arguments.Contains('"D:\fresh copies\\"') -or $weekly.Arguments -match '-replace') { throw 'Incorrect weekly command line.' }
    $batch = & $install @base @settings -Mode WeeklyVerify -RunWhenLoggedOff
    if ($batch.LogonType -ne 'S4U') { throw 'Incorrect weekly batch principal.' }
    $monitor = & $install @base -Mode Monitor
    if ($monitor.Arguments -ne ('"-home" "' + $dir + '" "-monitor"') -or $monitor.TaskName -eq $weekly.TaskName) { throw 'Incorrect monitor command line.' }
    $settings.MeasurementDatabase = 'different'
    $rejected = $false
    try { & $install @base @settings -Mode WeeklyVerify } catch { $rejected = $true }
    if (-not $rejected) { throw 'Profile drift was ignored.' }
    $settings.MeasurementDatabase = 'measurement'
    $chosenTime = & $install @base @settings -Mode WeeklyVerify -At '17:30'
    if ($chosenTime.At -ne '17:30') { throw 'Chosen schedule time was ignored.' }
    # Substitute all scheduler cmdlets. This exercises the registration branch
    # without installing a task or launching SQL on the Windows CI worker.
    function New-ScheduledTaskAction { param($Execute,$Argument,$WorkingDirectory) $PSBoundParameters }
    function New-ScheduledTaskTrigger { param([switch]$Weekly,$WeeksInterval,$DaysOfWeek,$At,[switch]$AtLogOn,$User) $PSBoundParameters }
    function New-ScheduledTaskSettingsSet { param($MultipleInstances,$ExecutionTimeLimit) $PSBoundParameters }
    function New-ScheduledTaskPrincipal { param($UserId,$LogonType,$RunLevel) $PSBoundParameters }
    function Register-ScheduledTask { param($TaskName,$Action,$Trigger,$Principal,$Settings,$ErrorAction) $PSBoundParameters }
    $base.Remove('Preview')
    $task = & $install @base @settings -Mode WeeklyVerify -Confirm:$false
    if (-not $task.Trigger.Weekly -or $task.Trigger.WeeksInterval -ne 1 -or $task.Trigger.At -ne '04:00' -or
        $task.Action.Argument -ne $weekly.Arguments -or $task.Principal.LogonType -ne 'Interactive' -or
        $task.Settings.MultipleInstances -ne 'IgnoreNew') { throw 'Unexpected weekly registration.' }
    $task = & $install @base -Mode Monitor -Confirm:$false
    if (-not $task.Trigger.AtLogOn -or $task.Action.Argument -ne $monitor.Arguments) { throw 'Unexpected monitoring registration.' }
    Write-Output 'Weekly and monitoring task plans and registration passed.'
} finally {
    if (Test-Path $dir) { Remove-Item -LiteralPath $dir -Recurse -Force }
}
