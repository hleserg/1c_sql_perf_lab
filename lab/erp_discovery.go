package lab

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

//go:embed erp_com.ps1
var erpCOMScript string

//go:embed discover_document.ps1
var discoverDocumentPS string
var metadataName = regexp.MustCompile(`^[\pL_][\pL\pN_]{0,127}$`)

type erpFixture struct {
	Name, ID, AsOf string
	Post           bool `json:",omitempty"`
}

type erpPlan struct {
	Documents    []erpFixture
	Missing      []string
	Product      string
	StockDay     string
	StockProduct string
}

func (p Profile) singleDocument() bool {
	return p.DocumentUUID != "" && metadataName.MatchString(p.Document) && uuid.MatchString(p.DocumentUUID)
}

func (p Profile) builtinSource() string {
	if p.singleDocument() {
		return "1С · чтение одного документа · " + p.Document
	}
	if p.erpPlan == nil {
		return "1С · операции документов"
	}
	label := fmt.Sprintf("1С · %d видов документов · чтение, списки и сводки", len(p.erpPlan.Documents))
	if len(p.ERPPostDocuments) > 0 {
		label += fmt.Sprintf(" · перепроведение %d видов с откатом", len(p.ERPPostDocuments))
	}
	if p.ERPStockReport {
		label += " · складская ведомость"
	}
	return label
}

// FindWorkloadDocument resolves a single reference for existing API callers.
func FindWorkloadDocument(ctx context.Context, p Profile) (string, error) {
	if !metadataName.MatchString(p.Document) {
		return "", errors.New("некорректное имя вида документа")
	}
	p.ERPDocuments = []string{p.Document}
	p.DocumentUUID = ""
	p.ERPPostDocuments = nil
	p.ERPStockReport = false
	if p.TimeoutSeconds == 0 {
		p.TimeoutSeconds = 25
	}
	found, err := PrepareERPWorkload(ctx, p)
	if err != nil {
		return "", err
	}
	return found.erpPlan.Documents[0].ID, nil
}

// Resolve fixtures once per run. Reference values stay in the private worker environment.
func PrepareERPWorkload(ctx context.Context, p Profile) (Profile, error) {
	if p.ERPServer == "" || p.ERPDatabase == "" {
		return p, errors.New("укажи сервер и базу 1С")
	}
	if p.DocumentUUID != "" {
		if !p.singleDocument() || len(p.ERPPostDocuments) > 0 {
			return p, errors.New("проверь вид и UUID документа: одиночный сценарий поддерживает только чтение")
		}
		p.erpPlan = &erpPlan{Documents: []erpFixture{{Name: p.Document, ID: p.DocumentUUID}}}
		return p, nil
	}
	names := p.ERPDocuments
	if len(names) == 0 {
		names = []string{"ЭтапПроизводства2_2", "ДвижениеПродукцииИМатериалов", "ЗаказНаПроизводство2_2", "ЗаказКлиента", "РеализацияТоваровУслуг", "ПриобретениеТоваровУслуг", "ПеремещениеТоваров"}
		if p.Document != "" {
			names = append([]string{p.Document}, names...)
		}
	}
	var unique []string
	seen := map[string]bool{}
	for _, n := range names {
		if !metadataName.MatchString(n) {
			return p, errors.New("некорректное имя вида документа")
		}
		if !seen[n] {
			unique = append(unique, n)
			seen[n] = true
		}
	}
	if len(unique) > 64 {
		return p, errors.New("выбери не более 64 видов документов")
	}
	post := map[string]bool{}
	for _, n := range p.ERPPostDocuments {
		if !seen[n] || !LocalERPHost(p) {
			return p, errors.New("для проведения выбери вид из ERPDocuments на локальном стенде")
		}
		post[n] = true
	}
	if len(post) > 0 {
		found, err := DiscoverStandERP(ctx, p)
		if err != nil {
			return p, err
		}
		p = found
		var processes []platformProcess
		if err := localAdminJSON(ctx, rasHostScript, &processes); err != nil {
			return p, err
		}
		ports := processPorts(processes, "ragent.exe")
		if len(ports) > 1 {
			return p, errors.New("для проведения требуется один локальный агент 1С")
		}
		p.erpAgentPort = "1540"
		for port := range ports {
			p.erpAgentPort = fmt.Sprint(port)
		}
	}
	postNames, _ := json.Marshal(p.ERPPostDocuments)
	input, _ := json.Marshal(unique)
	ctx, stop := context.WithTimeout(ctx, p.Timeout())
	defer stop()
	cmd, err := platformPowerShell(ctx, erpCOMScript+"\n"+discoverDocumentPS)
	if err != nil {
		return p, err
	}
	env, err := erpLoginEnv(p)
	if err != nil {
		return p, err
	}
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "PERFLAB_ERP_SERVER="+p.ERPServer, "PERFLAB_ERP_DATABASE="+p.ERPDatabase, "PERFLAB_DOCUMENTS="+string(input), "PERFLAB_POST="+string(postNames))
	if p.ERPStockReport {
		cmd.Env = append(cmd.Env, "PERFLAB_STOCK_REPORT=1")
	} else {
		cmd.Env = append(cmd.Env, "PERFLAB_STOCK_REPORT=0")
	}
	var stderr racOutput
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return p, ctx.Err()
		}
		return p, fmt.Errorf("%s · %s: %w", p.ERPDatabase, workloadFailure(stderr.data.Bytes()), err)
	}
	var plan erpPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		return p, errors.New("некорректный ответ при подборе документов")
	}
	if len(plan.Documents) == 0 {
		return p, errors.New("не найдено ни одного доступного документа для замеров")
	}
	for i, d := range plan.Documents {
		if !seen[d.Name] || !uuid.MatchString(d.ID) {
			return p, errors.New("некорректная контрольная ссылка")
		}
		if _, err := time.Parse("2006-01-02T15:04:05", d.AsOf); err != nil {
			return p, errors.New("некорректная контрольная дата")
		}
		plan.Documents[i].Post = post[d.Name]
		delete(post, d.Name)
	}
	if len(post) > 0 {
		return p, errors.New("не найдены проведённые документы всех выбранных видов")
	}
	if plan.Product != "" && !uuid.MatchString(plan.Product) {
		return p, errors.New("некорректная ссылка номенклатуры")
	}
	if p.ERPStockReport {
		if _, err := time.Parse("2006-01-02", plan.StockDay); err != nil || !uuid.MatchString(plan.StockProduct) {
			return p, errors.New("для складской ведомости требуется номенклатура с движениями и дата")
		}
	}
	p.erpPlan = &plan
	return p, nil
}

func (p Profile) WorkloadSummary() string {
	s := p.builtinSource()
	if p.erpPlan != nil {
		b, _ := json.Marshal(p.erpPlan.Documents)
		sum := sha256.Sum256(b)
		s += fmt.Sprintf(" · набор документов %x", sum[:8])
		if p.erpPlan.StockDay != "" {
			s += " · день ведомости " + p.erpPlan.StockDay
		}
	}
	if p.erpPlan != nil && len(p.erpPlan.Missing) > 0 {
		s += " · без данных: " + strings.Join(p.erpPlan.Missing, ", ")
	}
	return s
}

var workloadCode = regexp.MustCompile(`^0x[0-9a-fA-F]{8}$`)

func workloadFailure(stderr []byte) string {
	var fields []string
	for _, line := range strings.Split(string(stderr), "\n") {
		if strings.HasPrefix(line, "PERFLAB-COM|") {
			fields = strings.Split(strings.TrimSpace(line), "|")
			break
		}
	}
	if len(fields) != 4 || fields[0] != "PERFLAB-COM" {
		return "процесс Windows завершился без диагностики этапа COM. Проверь Windows PowerShell x64 на узле 1С"
	}
	stages := map[string]string{
		"activate": "создание COM-коннектора", "connect": "вход в информационную базу",
		"query": "запрос ссылки документа", "uuid": "получение UUID выбранной ссылки",
		"reference": "поиск ссылки по UUID", "read": "чтение объекта по ссылке",
		"list": "чтение списка документов", "summary": "сводка по документам", "lookup": "подбор номенклатуры",
		"guard": "проверка блокировки обмена", "post": "перепроведение документа", "rollback": "откат перепроведения",
	}
	stages["stock"] = "формирование складской ведомости"
	stage, ok := stages[fields[1]]
	if !ok {
		return "неопознанный этап сценария COM"
	}
	reason := map[string]string{
		"registration": "проверь регистрацию COM текущей сборки платформы",
		"login":        "платформа отклонила вход. Проверь пользователя и способ входа в базу 1С",
		"access":       "платформа сообщила об ограничении прав. Проверь права пользователя 1С на эту операцию",
		"metadata":     "платформа не нашла таблицу, поле или метод. Сверь метаданные базы и версию сценария",
		"licence":      "проверь доступность лицензии для внешнего соединения",
		"network":      "проверь доступность сервера 1С по адресу регистрации",
		"empty":        "документ по выбранной ссылке больше не доступен. Повтори подбор документа",
	}[fields[2]]
	if fields[1] == "guard" && reason == "" {
		reason = "проверь запрет регламентных заданий, блокировку внешних ресурсов БСП и отключение синхронизации данных"
	}
	if reason == "" {
		reason = "сверь исключение в технологическом журнале 1С по времени этого этапа"
	}
	if workloadCode.MatchString(fields[3]) {
		stage += " (" + fields[3] + ")"
	}
	return "COM · " + stage + " · " + reason
}
