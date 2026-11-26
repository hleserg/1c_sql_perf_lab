$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
function Quote-1C([string]$value) { return '"' + $value.Replace('"', '""') + '"' }

# Only stage, known cause and HRESULT leave this process. COM messages can carry
# connection strings. Inspect inner exceptions before assigning a category.
function Write-ErpFailure([string]$stage, $failure) {
    $ex = $failure.Exception
    $messages = @()
    $code = ''
    while ($ex) {
        $messages += [string]$ex.Message
        if ($ex -is [Runtime.InteropServices.COMException] -or !$code) {
            $code = '0x{0:X8}' -f $ex.HResult
        }
        $ex = $ex.InnerException
    }
    $message = $messages -join ' '
    if ($message -match '(?i)0x[0-9a-f]{8}\b') { $code = $Matches[0] }
    $cause = switch -Regex ($message) {
        '0x80040154|0x8002801d|class not registered|класс не зарегистрирован' { 'registration'; break }
        'недостаточно прав|нарушение прав|access (is )?denied|permission denied|нет прав' { 'access'; break }
        'аутентификац|идентификац.*пользовател|authentication|invalid (user|password)|неверн.*(имя|пароль)|неправильн.*(имя|пароль)|имя пользователя не определено|пользователь не обнаружен' { 'login'; break }
        'таблица не найдена|поле не найдено|table not found|field not found|does not contain a method|метод объекта не обнаружен' { 'metadata'; break }
        'лицензи|licen[sc]e' { 'licence'; break }
        '10061|10060|connection refused|соединение.*отверг|недоступен сервер' { 'network'; break }
        default { 'unclassified' }
    }
    [Console]::Error.WriteLine("PERFLAB-COM|$stage|$cause|$code")
}

try {
    $stage = 'activate'
    $connector = New-Object -ComObject V83.COMConnector
    $stage = 'connect'
    $connection = 'Srvr=' + (Quote-1C $env:PERFLAB_ERP_SERVER) + ';Ref=' + (Quote-1C $env:PERFLAB_ERP_DATABASE) + ';'
    if ($env:PERFLAB_ERP_USER) {
        $connection += 'Usr=' + (Quote-1C $env:PERFLAB_ERP_USER) + ';Pwd=' + (Quote-1C $env:PERFLAB_ERP_PASSWORD) + ';'
    }
    $session = $connector.Connect($connection)
} catch {
    Write-ErpFailure $stage $_
    exit 1
}
