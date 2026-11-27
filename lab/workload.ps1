try {
    $stage = 'reference'
    $plan = $env:PERFLAB_PLAN | ConvertFrom-Json
    if (@($plan.Documents | Where-Object Post).Count -gt 0) {
        $stage = 'guard'
        Assert-PostingIsolation
    }
    $documents = Read-ErpMember $session Documents
    $data = @{}
    $proof = [Text.StringBuilder]::new()
    foreach ($fixture in $plan.Documents) {
        $name = $fixture.Name
        $manager = Read-ErpMember $documents $name
        $uuid = Read-ErpMember $session NewObject @('UUID', $fixture.ID)
        $ref = Read-ErpMember $manager GetRef @($uuid)
        $stage = 'read'
        $timer = [Diagnostics.Stopwatch]::StartNew()
        $object = Read-ErpMember $ref GetObject @()
        $timer.Stop()
        if ($null -eq $object -or $object -is [DBNull]) { [Console]::Error.WriteLine('PERFLAB-COM|read|empty|'); exit 1 }
        $data["Чтение документа · $name"] = $timer.Elapsed.TotalMilliseconds
        if ($fixture.Post) {
            $stage = 'post'
            $null = Read-ErpMember $session BeginTransaction @()
            try {
                $doc = Read-ErpMember $ref GetObject @()
                if (!(Read-ErpMember $doc Posted)) { throw 'Expected a posted document' }
                $version = Read-ErpMember $doc DataVersion
                $server = Read-ErpMember $session ОбщегоНазначенияВызовСервера
                $refs = Read-ErpMember $session NewObject @('Array')
                $null = Read-ErpMember $refs Add @($ref)
                $timer.Restart()
                $failed = Read-ErpMember $server ПровестиДокументы (,$refs)
                $timer.Stop()
                if ((Read-ErpMember $failed Count @()) -ne 0) { throw 'Posting refused' }
                $elapsed = $timer.Elapsed.TotalMilliseconds
            } finally {
                $null = Read-ErpMember $session RollbackTransaction @()
            }
            $stage = 'rollback'
            $restored = Read-ErpMember $ref GetObject @()
            if (!(Read-ErpMember $restored Posted) -or (Read-ErpMember $restored DataVersion) -ne $version) {throw 'Document changed'}
            $data["Перепроведение с откатом · $name"] = $elapsed
        }
        if (!$fixture.AsOf) { continue }

        $end = [datetime]::ParseExact($fixture.AsOf, 'yyyy-MM-ddTHH:mm:ss', [Globalization.CultureInfo]::InvariantCulture)
        $stage = 'list'
        $q = New-ErpQuery ("ВЫБРАТЬ РАЗРЕШЕННЫЕ ПЕРВЫЕ 25 Д.Ссылка, Д.Номер, Д.Дата, Д.Проведен ИЗ Документ.$name КАК Д " +
            "ГДЕ Д.Дата <= &Конец И НЕ Д.ПометкаУдаления УПОРЯДОЧИТЬ ПО Д.Дата УБЫВ, Д.Ссылка УБЫВ")
        $null = Read-ErpMember $q SetParameter @('Конец', $end)
        $timer.Restart()
        $rows = Read-ErpMember (Read-ErpMember $q Execute @()) Unload @()
        $count = Read-ErpMember $rows Count @()
        $timer.Stop()
        if ($count -lt 1) { throw 'No list rows' }
        foreach ($row in $rows) {
            $null = $proof.Append((Get-ErpUUID (Read-ErpMember $row Ссылка))).Append('/').Append((Read-ErpMember $row Проведен)).Append(';')
        }
        $data["Список до 25 документов · $name"] = $timer.Elapsed.TotalMilliseconds

        $stage = 'summary'
        $q = New-ErpQuery ("ВЫБРАТЬ РАЗРЕШЕННЫЕ Д.Проведен, КОЛИЧЕСТВО(*) КАК Количество ИЗ Документ.$name КАК Д " +
            "ГДЕ Д.Дата МЕЖДУ &Начало И &Конец И НЕ Д.ПометкаУдаления СГРУППИРОВАТЬ ПО Д.Проведен УПОРЯДОЧИТЬ ПО Д.Проведен")
        $null = Read-ErpMember $q SetParameter @('Начало', $end.AddDays(-30))
        $null = Read-ErpMember $q SetParameter @('Конец', $end)
        $timer.Restart()
        $rows = Read-ErpMember (Read-ErpMember $q Execute @()) Unload @()
        $count = Read-ErpMember $rows Count @()
        $timer.Stop()
        if ($count -lt 1) { throw 'No summary rows' }
        foreach ($row in $rows) {
            $null = $proof.Append((Read-ErpMember $row Проведен)).Append('=').Append((Read-ErpMember $row Количество)).Append(';')
        }
        $data["Сводка по проведённости за 30 дней · $name"] = $timer.Elapsed.TotalMilliseconds
        $stage = 'reference'
    }
    if ($plan.Product) {
        $stage = 'lookup'
        $manager = Read-ErpMember (Read-ErpMember $session Catalogs) Номенклатура
        $uuid = Read-ErpMember $session NewObject @('UUID', $plan.Product)
        $ref = Read-ErpMember $manager GetRef @($uuid)
        $code = Read-ErpMember $ref Code
        $q = New-ErpQuery 'ВЫБРАТЬ РАЗРЕШЕННЫЕ Н.Ссылка ИЗ Справочник.Номенклатура КАК Н ГДЕ Н.Код = &Код И НЕ Н.ПометкаУдаления'
        $null = Read-ErpMember $q SetParameter @('Код', $code)
        $timer = [Diagnostics.Stopwatch]::StartNew()
        $s = Read-ErpMember (Read-ErpMember $q Execute @()) Select @()
        $match = $false
        while (Read-ErpMember $s Next @()) {
            if ((Get-ErpUUID (Read-ErpMember $s Get @(0))) -eq $plan.Product) { $match = $true; break }
        }
        $timer.Stop()
        if (!$match) { throw 'Product changed' }
        $data['Подбор номенклатуры по коду'] = $timer.Elapsed.TotalMilliseconds
    }
    if ($plan.StockDay) {
        $stage = 'stock'
        $manager = Read-ErpMember (Read-ErpMember $session Catalogs) Номенклатура
        $uuid = Read-ErpMember $session NewObject @('UUID', $plan.StockProduct)
        $item = Read-ErpMember $manager GetRef @($uuid)
        $data['Отчёт · Ведомость по товарам на складах'] = Measure-StockReport $item $plan.StockDay $proof
    }
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $digest = [Convert]::ToBase64String($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($proof.ToString()))) }
    finally { $sha.Dispose() }
    [Console]::WriteLine((ConvertTo-Json -InputObject @{Times=$data; Signature=$digest} -Depth 3 -Compress))
} catch {
    Write-ErpFailure $stage $_
    exit 1
}
