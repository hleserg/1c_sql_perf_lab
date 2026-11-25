package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The stand operator's adapter extracts start/end and completed restructure
// operations from the actual update log and names that log for verification.
type restructureProof struct {
	Version, Operations                                                                     int
	TrialID, Server, Database, DatabaseGUID, BackupSetUUID, PackageID, PackageSHA256, Event string
	Started, Finished                                                                       time.Time
	EvidencePath, EvidenceSHA256                                                            string
}
type restructureResult struct {
	started, finished time.Time
	exit              int
	proof             restructureProof
	verified          bool
	schema            string
}

func (r restructureResult) elapsed() int64 {
	return r.proof.Finished.Sub(r.proof.Started).Milliseconds()
}
func (r restructureResult) section(db string, dop int) Section {
	utc := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
	row := []string{db, strconv.Itoa(dop), utc(r.started), utc(r.finished), strconv.FormatInt(r.finished.Sub(r.started).Milliseconds(), 10), strconv.Itoa(r.exit), "Нет подтверждения", "—", "—", "—", "—"}
	if r.verified {
		row[6] = "Маркеры и файл проверены"
		row[7] = utc(r.proof.Started)
		row[8] = utc(r.proof.Finished)
		row[9] = strconv.Itoa(r.proof.Operations)
		row[10] = r.proof.EvidenceSHA256
	}
	return Section{ID: 33, Title: "Обновление · " + db, Columns: []string{"Копия", "MAXDOP базы", "Старт UTC", "Конец UTC", "Команда мс", "Код выхода", "Журнал обновления", "Начало реструктуризации UTC", "Конец реструктуризации UTC", "Завершённых операций", "SHA-256 журнала"}, Rows: [][]string{row}}
}

type proofOutput struct{ buf bytes.Buffer }

func (w *proofOutput) Write(b []byte) (int, error) {
	if len(b) > 65536-w.buf.Len() {
		return 0, errors.New("результат адаптера превысил 64 КиБ")
	}
	return w.buf.Write(b)
}

func (p RestructurePlan) runAdapter(ctx context.Context, db, guid, nonce string, dop int) (r restructureResult, err error) {
	one, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(one, p.AdapterPath, p.AdapterArgs...)
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = append(os.Environ(), "PERFLAB_TRIAL_ID="+nonce, "PERFLAB_SQL_SERVER="+p.Server, "PERFLAB_SQL_DATABASE="+db, "PERFLAB_DATABASE_GUID="+guid, "PERFLAB_BACKUP_UUID="+p.BackupSetUUID,
		"PERFLAB_PACKAGE="+p.PackagePath, "PERFLAB_PACKAGE_ID="+p.PackageID, "PERFLAB_PACKAGE_SHA256="+p.PackageSHA256, "PERFLAB_MAXDOP="+strconv.Itoa(dop))
	var output proofOutput
	cmd.Stdout = &output
	r.started = time.Now()
	r.exit = -1
	err = runUpdateProcess(cmd)
	r.finished = time.Now()
	if cmd.ProcessState != nil {
		r.exit = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		if one.Err() != nil {
			return r, one.Err()
		}
		return r, fmt.Errorf("адаптер обновления: %w", err)
	}
	d := json.NewDecoder(&output.buf)
	d.DisallowUnknownFields()
	if e := d.Decode(&r.proof); e != nil {
		return r, errors.New("адаптер должен вернуть один JSON с подтверждением реструктуризации")
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		return r, errors.New("лишние данные после результата адаптера")
	}
	if err = p.validateProof(r, db, guid, nonce); err != nil {
		return r, err
	}
	if err = verifyTrialFile(ctx, r.proof.EvidencePath, r.proof.EvidenceSHA256); err != nil {
		return r, fmt.Errorf("файл журнала операции не прошёл проверку: %w", err)
	}
	if err = p.verifyFiles(ctx); err != nil {
		return r, err
	}
	r.verified = true
	return r, nil
}
func (p RestructurePlan) validateProof(r restructureResult, db, guid, nonce string) error {
	f := r.proof
	if f.Version != 1 || f.Event != "restructure-completed" || f.TrialID != nonce || !strings.EqualFold(f.Server, p.Server) || f.Database != db || !strings.EqualFold(f.DatabaseGUID, guid) || !strings.EqualFold(f.BackupSetUUID, p.BackupSetUUID) || f.PackageID != p.PackageID || !strings.EqualFold(f.PackageSHA256, p.PackageSHA256) {
		return errors.New("маркеры адаптера относятся к другому запуску, базе или поставке")
	}
	// Accommodate millisecond timestamps printed by an adapter on this host.
	if f.Operations <= 0 || f.Started.Before(r.started.Add(-time.Millisecond)) || f.Finished.After(r.finished.Add(time.Millisecond)) || !f.Finished.After(f.Started) {
		return errors.New("адаптер не подтвердил интервал и выполненные операции реструктуризации внутри запуска")
	}
	if !filepath.IsAbs(f.EvidencePath) || len(f.EvidenceSHA256) != 64 || strings.EqualFold(f.EvidenceSHA256, p.PackageSHA256) || strings.EqualFold(f.EvidenceSHA256, p.AdapterSHA256) {
		return errors.New("нужны отдельный фактический файл журнала операции и его SHA-256")
	}
	return nil
}
