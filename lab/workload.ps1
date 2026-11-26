try {
    $stage = 'reference'
    $manager = $session.Documents.($env:PERFLAB_DOCUMENT)
    $id = $session.NewObject('UUID', $env:PERFLAB_UUID)
    $ref = $manager.GetRef($id)
    $stage = 'read'
    $timer = [Diagnostics.Stopwatch]::StartNew()
    $obj = $ref.GetObject()
    $timer.Stop()
    if ($null -eq $obj) {
        [Console]::Error.WriteLine('PERFLAB-COM|read|empty|')
        exit 1
    }
    $data = @{}
    $data[('Чтение документа · ' + $env:PERFLAB_DOCUMENT)] = $timer.Elapsed.TotalMilliseconds
    [Console]::WriteLine(($data | ConvertTo-Json -Compress))
} catch {
    Write-ErpFailure $stage $_
    exit 1
}
