#requires -Version 5.1
[CmdletBinding(SupportsShouldProcess=$true, ConfirmImpact='High')]
param(
    [Parameter(Mandatory)][ValidateSet('WeeklyVerify','Monitor')][string]$Mode,
    [Parameter(Mandatory)][string]$Executable,
    [Parameter(Mandatory)][string]$LabHome,
    [string]$ExpectedServer,
    [string]$MeasurementDatabase,
    [string]$BackupSource,
    [string]$BackupFolder,
    [ValidateRange(1,168)][int]$MaxAgeHours = 168,
    [DayOfWeek]$Day = 'Sunday',
    [ValidatePattern('^([01][0-9]|2[0-3]):[0-5][0-9]$')][string]$At = '04:00',
    [switch]$RunWhenLoggedOff,
    [switch]$Preview
)
$ErrorActionPreference = 'Stop'
# Task Scheduler accepts one Windows command line. Escape all arguments using
# CommandLineToArgvW rules, including trailing backslashes in quoted paths.
function Quote-Argument([string]$Value) {
    if ($Value -match '[\x00-\x1f]') { throw 'Control characters are not valid in scheduled arguments.' }
    $quoted = $Value -replace '(\\*)"', '$1$1\"'
    return '"' + ($quoted -replace '(\\+)$', '$1$1') + '"'
}
$exe = (Get-Item -LiteralPath $Executable -ErrorAction Stop)
if ($exe.PSIsContainer -or $exe.Extension -ine '.exe') { throw 'Executable must be a local .exe file.' }
$root = (Get-Item -LiteralPath $LabHome -ErrorAction Stop)
if (-not $root.PSIsContainer) { throw 'LabHome must be a directory.' }
$profile = Get-Content -Raw -Encoding UTF8 -LiteralPath (Join-Path $root.FullName 'local/stand.json') | ConvertFrom-Json
if ($Mode -eq 'Monitor' -and $RunWhenLoggedOff) { throw 'The monitor uses a logon trigger.' }
$logonType = if ($RunWhenLoggedOff) { 'S4U' } else { 'Interactive' }
$arguments = @('-home',$root.FullName,'-monitor')
if ($Mode -eq 'WeeklyVerify') {
    if ([string]::IsNullOrWhiteSpace($ExpectedServer) -or [string]::IsNullOrWhiteSpace($MeasurementDatabase) -or
        [string]::IsNullOrWhiteSpace($BackupSource) -or [string]::IsNullOrWhiteSpace($BackupFolder)) {
        throw 'WeeklyVerify requires ExpectedServer, MeasurementDatabase, BackupSource and BackupFolder.'
    }
    if ($profile.ExpectedServer -ine $ExpectedServer -or $profile.Database -ine $MeasurementDatabase) {
        throw 'The measurement profile differs from the explicitly selected instance or database.'
    }
    if ($BackupFolder -notmatch '^([A-Za-z]:[\\/]|\\\\[^\\]+\\[^\\]+|/)') { throw 'BackupFolder must be an absolute SQL-server path.' }
    $arguments = @('weekly-verify','-home',$root.FullName,'-server',$ExpectedServer,'-measurement',$MeasurementDatabase,
        '-source',$BackupSource,'-folder',$BackupFolder,'-max-age',("${MaxAgeHours}h"))
}
$command = ($arguments | ForEach-Object { Quote-Argument $_ }) -join ' '
$hash = [Security.Cryptography.SHA256]::Create()
try {
    $bytes = [Text.Encoding]::UTF8.GetBytes($root.FullName.ToUpperInvariant())
    $suffix = [BitConverter]::ToString($hash.ComputeHash($bytes)).Replace('-','').Substring(0,12)
} finally { $hash.Dispose() }
$name = "PerfLab-$Mode-$suffix"
$plan = [PSCustomObject]@{ TaskName=$name; Execute=$exe.FullName; Arguments=$command; Mode=$Mode; Day=$Day; At=$At; LogonType=$logonType }
if ($Preview) { return $plan }
if (-not $PSCmdlet.ShouldProcess($name, "Register scheduled task using $($exe.FullName) $command")) { return }
# S4U permits a passwordless local batch logon. It requires local SQL access;
# remote SQL or encrypted folders may require a separately provisioned task identity.
$user = [Security.Principal.WindowsIdentity]::GetCurrent().Name
$action = New-ScheduledTaskAction -Execute $exe.FullName -Argument $command -WorkingDirectory $root.FullName
if ($Mode -eq 'WeeklyVerify') {
    $trigger = New-ScheduledTaskTrigger -Weekly -WeeksInterval 1 -DaysOfWeek $Day -At $At
} else {
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $user
}
# Do not start missed heavy work later in an arbitrary window.
$settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -ExecutionTimeLimit ([TimeSpan]::Zero)
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType $logonType -RunLevel Limited
Register-ScheduledTask -TaskName $name -Action $action -Trigger $trigger -Principal $principal -Settings $settings -ErrorAction Stop
