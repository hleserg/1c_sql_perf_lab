$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
function Quote-1C([string]$v) { return '"' + $v.Replace('"','""') + '"' }
$conn = New-Object -ComObject V83.COMConnector
$str = 'Srvr=' + (Quote-1C $env:PERFLAB_ERP_SERVER) + ';Ref=' + (Quote-1C $env:PERFLAB_ERP_DATABASE) + ';'
if ($env:PERFLAB_ERP_USER) { $str += 'Usr=' + (Quote-1C $env:PERFLAB_ERP_USER) + ';Pwd=' + (Quote-1C $env:PERFLAB_ERP_PASSWORD) + ';' }
$session = $conn.Connect($str)
$query = $session.NewObject('Query')
$query.Text = 'ВЫБРАТЬ ПЕРВЫЕ 1 Д.Ссылка КАК ВыбранныйДокумент ИЗ Документ.' + $env:PERFLAB_DOCUMENT + ' КАК Д'
$row = $query.Execute().Select()
if ($row.Next()) { [Console]::WriteLine($session.XMLString($row.ВыбранныйДокумент.UUID())) }
