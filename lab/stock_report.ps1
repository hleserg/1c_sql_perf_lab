function Measure-StockReport($ref, [string]$date, $proof) {
    $day = [datetime]::ParseExact($date, 'yyyy-MM-dd', [Globalization.CultureInfo]::InvariantCulture)
    $manager = Read-ErpMember (Read-ErpMember $session Reports) ВедомостьПоТоварамНаСкладах
    $report = Read-ErpMember $manager Create @()
    $schema = Read-ErpMember $report GetTemplate @('ОсновнаяСхемаКомпоновкиДанных')
    $composer = Read-ErpMember $report КомпоновщикНастроек
    $settings = Read-ErpMember $schema НастройкиПоУмолчанию
    $null = Read-ErpMember $composer LoadSettings (,$settings)
    $active = Read-ErpMember $composer Настройки
    $period = Read-ErpMember $session NewObject @('СтандартныйПериод')
    $period.ДатаНачала = $day
    $period.ДатаОкончания = $day.AddDays(1).AddSeconds(-1)
    $null = Read-ErpMember (Read-ErpMember $active ПараметрыДанных) УстановитьЗначениеПараметра @('Период',$period)
    $items = Read-ErpMember (Read-ErpMember $active Отбор) Элементы
    $desc = Read-ErpMember $session NewObject @('ОписаниеТипов','ЭлементОтбораКомпоновкиДанных')
    $kind = Read-ErpMember (Read-ErpMember $desc Типы @()) Получить @(0)
    $term = Read-ErpMember $items Добавить (,$kind)
    $term.ЛевоеЗначение = Read-ErpMember $session NewObject @('ПолеКомпоновкиДанных','Номенклатура')
    $term.ПравоеЗначение = $ref
    $term.ВидСравнения = Read-ErpMember (Read-ErpMember $session ВидСравненияКомпоновкиДанных) Равно
    $term.Использование = $true
    $output = Read-ErpMember $session NewObject @('ТабличныйДокумент')
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $null = Read-ErpMember $report СкомпоноватьРезультат (,$output)
    $watch.Stop()
    $height = Read-ErpMember $output TableHeight
    if ($height -le 0) {throw 'Empty stock report'}
    $null = $proof.Append('report=').Append($height)
    return $watch.Elapsed.TotalMilliseconds
}
