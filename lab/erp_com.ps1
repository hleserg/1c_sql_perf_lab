$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
function Quote-1C([string]$value) { return '"' + $value.Replace('"', '""') + '"' }

# PS 5.1's COM adapter can wrap dynamic 1C references in PSObject. Pass
# the underlying IDispatch object to 1C, including when converting a UUID.
function Read-ErpMember($obj, [string]$name, [object[]]$arguments = $null) {
    $flags = [Reflection.BindingFlags]::GetProperty
    if ($null -ne $arguments) {
        $flags = [Reflection.BindingFlags]::InvokeMethod
        for ($i = 0; $i -lt $arguments.Count; $i++) { $arguments[$i] = $arguments[$i].PSObject.BaseObject }
    }
    return ,([type]::GetType('System.__ComObject').InvokeMember($name, $flags, $null, $obj, $arguments))
}

function Get-ErpUUID($ref) {
    $id = Read-ErpMember $ref UUID @()
    return Read-ErpMember $session XMLString @($id)
}

function New-ErpQuery([string]$text) {
    return ,(Read-ErpMember $session NewObject @('Query', $text))
}

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
