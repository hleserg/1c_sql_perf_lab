package lab

import (
	"strconv"
	"strings"
)

const RestoreReportTitle = "Восстановление из копии"

type RestoreStage string

const (
	RestorePrepare   RestoreStage = "prepare"
	RestoreVerify    RestoreStage = "verify"
	RestoreRecheck   RestoreStage = "recheck"
	RestoreExclusive RestoreStage = "exclusive"
	RestoreWrite     RestoreStage = "write"
	RestoreAccess    RestoreStage = "access"
	RestoreReady     RestoreStage = "ready"
)

// Percent is the last DMV observation. Complete records a successful SQL command,
// including short commands that finish before the first sample.
type RestoreMeter struct {
	Percent  *int `json:",omitempty"`
	Complete bool `json:",omitempty"`
}

type RestoreProgress struct {
	Stage         RestoreStage
	Verify, Write RestoreMeter
	State         string `json:",omitempty"` // State confirmed by SQL after the operation.
}

type restoreLog struct {
	p    RestoreProgress
	emit func(string, RestoreProgress)
}

func (l *restoreLog) note(s string) { l.emit(s, l.p) }

func (l *restoreLog) stage(s RestoreStage, detail string) {
	l.p.Stage = s
	l.note(detail)
}

func (l *restoreLog) sample(pct int) {
	label := "Проверка копии"
	switch l.p.Stage {
	case RestoreVerify:
		l.p.Verify.Percent = &pct
	case RestoreWrite:
		l.p.Write.Percent = &pct
		label = "Восстановление"
	default:
		return
	}
	l.note(label + " · " + strconv.Itoa(pct) + "%")
}

// Older saved reports only have journal messages. Infer facts only from messages
// issued by the old runner at the corresponding SQL operation.
func (r Report) RestoreStatus() RestoreProgress {
	if r.Restore != nil {
		return *r.Restore
	}
	var p RestoreProgress
	for _, s := range r.Journal {
		switch {
		case strings.HasPrefix(s, "Подключение к "):
			p.Stage = RestorePrepare
		case strings.HasPrefix(s, "Проверка носителя RESTORE VERIFYONLY"), strings.HasPrefix(s, "Демо: проверка"):
			p.Stage = RestoreVerify
		case strings.HasPrefix(s, "Отключение сеансов базы "), strings.HasPrefix(s, "Демо: отключение"):
			p.Stage = RestoreExclusive
			p.Verify.Complete = true
		case strings.HasPrefix(s, "Проверка копии · "):
			p.Stage = RestoreVerify
			p.Verify.Percent = restoreJournalPercent(s)
		case strings.HasPrefix(s, "Восстановление · "):
			p.Stage = RestoreWrite
			p.Verify.Complete = true
			p.Write.Percent = restoreJournalPercent(s)
		case strings.HasPrefix(s, "Восстановление ") && strings.Contains(s, " · набор "), strings.HasPrefix(s, "Демо: восстановление"):
			p.Stage = RestoreWrite
			p.Verify.Complete = true
		case strings.HasPrefix(s, "Состояние после ошибки: "):
			p.State = strings.TrimPrefix(s, "Состояние после ошибки: ")
		case s == "SQL подтвердил ONLINE / MULTI_USER", s == "Демо: ONLINE / MULTI_USER":
			p.Stage = RestoreReady
			p.Verify.Complete = true
			p.Write.Complete = true
			p.State = "ONLINE / MULTI_USER"
		}
	}
	if strings.Contains(r.Error, "проверка копии до отключения сеансов") {
		p.Stage = RestoreVerify
	}
	return p
}

func restoreJournalPercent(s string) *int {
	_, val, ok := strings.Cut(s, " · ")
	if !ok || !strings.HasSuffix(val, "%") {
		return nil
	}
	pct, err := strconv.Atoi(strings.TrimSuffix(val, "%"))
	if err != nil || pct < 0 || pct > 100 {
		return nil
	}
	return &pct
}
