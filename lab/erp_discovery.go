package lab

import (
	"context"
	_ "embed"
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

// Pin one existing reference before the first sample so every session reads the same document.
func FindWorkloadDocument(ctx context.Context, p Profile) (string, error) {
	if p.ERPServer == "" || p.ERPDatabase == "" || !metadataName.MatchString(p.Document) {
		return "", fmt.Errorf("не заданы стендовая база 1С или имя документа")
	}
	ctx, stop := context.WithTimeout(ctx, 25*time.Second)
	defer stop()
	cmd, err := platformPowerShell(ctx, erpCOMScript+"\n"+discoverDocumentPS)
	if err != nil {
		return "", err
	}
	cmd.Env = append(os.Environ(), "PERFLAB_ERP_SERVER="+p.ERPServer, "PERFLAB_ERP_DATABASE="+p.ERPDatabase, "PERFLAB_DOCUMENT="+p.Document)
	var stderr racOutput
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("подбор документа %s в %s: %w", p.Document, p.ERPDatabase, ctx.Err())
		}
		return "", fmt.Errorf("%s / %s · %s: %w", p.ERPDatabase, p.Document, workloadFailure(stderr.data.Bytes()), err)
	}
	v := strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff"))
	if v == "" {
		return "", fmt.Errorf("в базе %s нет доступных для чтения документов вида %s. В профиле выбери вид с данными", p.ERPDatabase, p.Document)
	}
	if !uuid.MatchString(v) {
		return "", fmt.Errorf("платформа вернула некорректный UUID при подборе %s", p.Document)
	}
	return v, nil
}

var workloadCode = regexp.MustCompile(`^0x[0-9a-fA-F]{8}$`)

func workloadFailure(stderr []byte) string {
	fields := strings.Split(strings.TrimSpace(string(stderr)), "|")
	if len(fields) != 4 || fields[0] != "PERFLAB-COM" {
		return "процесс Windows завершился без диагностики этапа COM. Проверь Windows PowerShell x64 на узле 1С"
	}
	stages := map[string]string{
		"activate": "создание COM-коннектора", "connect": "вход в информационную базу",
		"query": "запрос ссылки документа", "uuid": "получение UUID выбранной ссылки",
		"reference": "поиск ссылки по UUID", "read": "чтение объекта по ссылке",
	}
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
	if reason == "" {
		reason = "сверь исключение в технологическом журнале 1С по времени этого этапа"
	}
	if workloadCode.MatchString(fields[3]) {
		stage += " (" + fields[3] + ")"
	}
	return "COM · " + stage + " · " + reason
}
