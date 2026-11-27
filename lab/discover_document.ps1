try {
    $stage = 'query'
    $fixtures = @()
    $missing = @()
    $meta = Read-ErpMember $session Metadata
    foreach ($name in ($env:PERFLAB_DOCUMENTS | ConvertFrom-Json)) {
        $kind = Read-ErpMember $meta FindByFullName @("Документ.$name")
        if ($null -eq $kind -or $kind -is [DBNull]) { $missing += $name; continue }
        $posted = if ($name -in ($env:PERFLAB_POST | ConvertFrom-Json)) { ' И Д.Проведен' } else { '' }
        $q = New-ErpQuery ("ВЫБРАТЬ РАЗРЕШЕННЫЕ ПЕРВЫЕ 1 Д.Ссылка, Д.Дата ИЗ Документ.$name КАК Д " +
            "ГДЕ НЕ Д.ПометкаУдаления И Д.Дата <= &Срез $posted УПОРЯДОЧИТЬ ПО Д.Дата УБЫВ, Д.Ссылка УБЫВ")
        $null = Read-ErpMember $q SetParameter @('Срез', [datetime]::Now)
        $s = Read-ErpMember (Read-ErpMember $q Execute @()) Select @()
        if (!(Read-ErpMember $s Next @())) { $missing += $name; continue }
        $stage = 'uuid'
        $ref = Read-ErpMember $s Get @(0)
        $date = Read-ErpMember $s Get @(1)
        $fixtures += @{Name=$name; ID=(Get-ErpUUID $ref); AsOf=$date.ToString('yyyy-MM-ddTHH:mm:ss')}
        $stage = 'query'
    }
    $product = ''
    $catalog = Read-ErpMember $meta FindByFullName @('Справочник.Номенклатура')
    $stockDay = ''
    $stockProduct = ''
    if ($env:PERFLAB_STOCK_REPORT -eq '1') {
        $stage = 'stock'
        $q = New-ErpQuery 'ВЫБРАТЬ РАЗРЕШЕННЫЕ ПЕРВЫЕ 1 Т.Номенклатура, Т.Период ИЗ РегистрНакопления.ТоварыНаСкладах КАК Т ГДЕ Т.Период <= &Срез И Т.Активность УПОРЯДОЧИТЬ ПО Т.Период УБЫВ, Т.Регистратор УБЫВ, Т.НомерСтроки'
        $null = Read-ErpMember $q SetParameter @('Срез',[datetime]::Now)
        $s = Read-ErpMember (Read-ErpMember $q Execute @()) Select @()
        if (!(Read-ErpMember $s Next @())) {throw 'No stock movement'}
        $stockProduct = Get-ErpUUID (Read-ErpMember $s Get @(0))
        $stockDay = (Read-ErpMember $s Get @(1)).ToString('yyyy-MM-dd')
    }
    if ($null -ne $catalog -and $catalog -isnot [DBNull]) {
        $q = New-ErpQuery 'ВЫБРАТЬ РАЗРЕШЕННЫЕ ПЕРВЫЕ 1 Н.Ссылка ИЗ Справочник.Номенклатура КАК Н ГДЕ НЕ Н.ПометкаУдаления И НЕ Н.ЭтоГруппа И Н.Код <> "" УПОРЯДОЧИТЬ ПО Н.Код, Н.Ссылка'
        $s = Read-ErpMember (Read-ErpMember $q Execute @()) Select @()
        if (Read-ErpMember $s Next @()) { $product = Get-ErpUUID (Read-ErpMember $s Get @(0)) }
    }
    [Console]::WriteLine((ConvertTo-Json -Compress -Depth 4 -InputObject @{Documents=$fixtures; Missing=$missing; Product=$product; StockDay=$stockDay; StockProduct=$stockProduct}))
} catch {
    Write-ErpFailure $stage $_
    exit 1
}
