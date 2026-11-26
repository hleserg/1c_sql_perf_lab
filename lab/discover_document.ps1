try {
    $stage = 'query'
    $query = $session.NewObject('Query')
    $query.Text = 'ВЫБРАТЬ РАЗРЕШЕННЫЕ ПЕРВЫЕ 1 Д.Ссылка КАК ВыбранныйДокумент ИЗ Документ.' + $env:PERFLAB_DOCUMENT + ' КАК Д'
    $selection = $query.Execute().Select()
    if (!$selection.Next()) { exit 0 }
    $stage = 'uuid'
    $ref = $selection.ВыбранныйДокумент
    $id = $ref.UUID()
    [Console]::WriteLine($session.XMLString($id))
} catch {
    Write-ErpFailure $stage $_
    exit 1
}
