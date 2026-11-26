package lab

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ItemResult keeps collected evidence even when a separate intervention for
// the same catalog item is unavailable or fails.
type ItemResult struct {
	ID                    int
	Title, Status, Reason string
	NextAction            string   `json:",omitempty"`
	Missing               []string `json:",omitempty"`
	Evidence              []string `json:",omitempty"`
	ElapsedMS             int64
}

// skippedStep records an expected missing prerequisite without making the
// enclosing run fail; keep the reason visible in both JSON and live progress.
type skippedStep struct{ reason string }

func (e skippedStep) Error() string { return e.reason }

type SuiteStep struct {
	Started              time.Time `json:",omitzero"`
	Name, Status, Detail string
	ElapsedMS            int64
}

const (
	statusPending  = "Не запускался"
	statusEvidence = "Собраны сведения"
	statusMeasured = "Измерено"
	statusDone     = "Выполнено"
	statusSkipped  = "Пропущено"
	statusFailed   = "Ошибка"
	statusManual   = "Требует отдельной проверки"
	statusSame     = "Цель уже установлена"
	statusControl  = "Измерено с откатом"
	suiteBaseline  = "Исходный замер"
)

func initSuite(rep *Report) {
	rep.Job = FullJob.Name
	for _, it := range All() {
		rep.Coverage = append(rep.Coverage, ItemResult{ID: it.ID, Title: it.Title, Status: statusPending, NextAction: nextAction(it.ID)})
	}
}
func finishCoverage(rep *Report, err error) {
	for i := range rep.Coverage {
		c := &rep.Coverage[i]
		if err != nil && !rep.SuiteComplete && c.Status == statusEvidence {
			switch c.ID {
			case 3, 11, 14, 15, 18, 19, 23, 24, 25, 28, 32, 34, 35, ClusterID:
				c.Reason += " Этап проверки или изменения не был завершён."
			}
		}
		if c.Status != statusPending {
			continue
		}
		if err != nil && !rep.SuiteComplete {
			c.Reason = "Прогон остановлен до этого пункта: " + err.Error()
			continue
		}
		c.Status = statusManual
		c.Reason = ByID(c.ID).Check
	}
}

// Verify the connection that owns the lock. Never reconnect to run an ALTER
// or recover on an identified-by-name replacement of the original database.
func (l *Live) VerifyIdentity(ctx context.Context, identity string) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var db, server, guid, lock string
	err := l.conn.QueryRowContext(ctx, `SELECT DB_NAME(), CONVERT(nvarchar(256),SERVERPROPERTY('ServerName')),
        CONVERT(varchar(36),service_broker_guid),APPLOCK_MODE('public','1c_sql_perf_lab','Session')
        FROM sys.databases WHERE database_id=DB_ID() AND database_id>4`).Scan(&db, &server, &guid, &lock)
	if err != nil {
		return err
	}
	if !strings.EqualFold(db, l.P.Database) || !strings.EqualFold(server, l.P.ExpectedServer) || !strings.EqualFold(guid, identity) || lock != "Exclusive" {
		return errors.New("изменился экземпляр, база, её идентификатор или блокировка эксперимента")
	}
	return nil
}

type suite struct {
	*experiment
	ctx        context.Context
	jobs       sqlJobs
	issues     []error
	usingERP   bool
	ready      bool
	maintained []int
	tested     []int
}

func (x *experiment) fullSuite(ctx context.Context) error {
	j, ok := x.run.Backend.(sqlJobs)
	if !ok {
		return errors.New("режим работ SQL недоступен")
	}
	s := suite{experiment: x, ctx: ctx, jobs: j}
	if x.journal.Identity == "" {
		return errors.New("пустой идентификатор базы стенда")
	}
	stages := []struct {
		name string
		run  func() error
	}{
		{"Инвентаризация и сверка", s.inspect},
		{"Исходная нагрузка", s.selectWorkload},
		{"Настройки и контроль отката", s.reversible},
		{"Проверка целостности", s.integrity},
		{"Обслуживание", s.maintenance},
		{"Совместный итог", s.finishMeasurements},
		{"Резервные копии", s.backup},
	}
	emit := x.run.Emit
	for i, stage := range stages {
		if emit != nil {
			x.run.Emit = func(ev Event) { ev.SuiteStage, ev.SuiteTotal, ev.SuiteName = i+1, len(stages), stage.name; emit(ev) }
		}
		if err := stage.run(); err != nil {
			return errors.Join(append(s.issues, err)...)
		}
	}
	s.rep.SuiteComplete = true
	return errors.Join(s.issues...)
}

func (s *suite) item(id int) *ItemResult {
	for i := range s.rep.Coverage {
		if s.rep.Coverage[i].ID == id {
			return &s.rep.Coverage[i]
		}
	}
	return nil
}
func (s *suite) mark(id int, status, reason string) {
	if c := s.item(id); c != nil {
		c.Status = status
		c.Reason = reason
	}
}
func (s *suite) record(sec Section) error {
	s.rep.Sections = append(s.rep.Sections, sec)
	s.run.say(Event{Phase: s.rep.Job, Detail: sec.Title, Section: &sec})
	if c := s.item(sec.ID); c != nil {
		if sec.Error == "" {
			c.Evidence = append(c.Evidence, sec.Title)
			if c.Status == statusPending || c.Status == statusSkipped {
				s.mark(sec.ID, statusEvidence, "Снят фактический результат проверки. Полный критерий пункта: "+ByID(sec.ID).Check)
			}
		} else {
			c.Missing = append(c.Missing, sec.Title+": "+sec.Error)
			if c.Status == statusPending {
				s.mark(sec.ID, statusSkipped, sec.Error)
			}
		}
	}
	return s.save()
}
func (s *suite) work(key string) error {
	j, _ := FindJob(key)
	start := len(s.rep.Sections)
	err := s.jobs.Work(s.ctx, key, s.record, func(m string) { s.run.say(Event{Phase: j.Name, Detail: m}) })
	if err != nil {
		return err
	}
	if start == len(s.rep.Sections) {
		return fmt.Errorf("%s: нет результата выполнения", j.Name)
	}
	var failures []error
	for _, sec := range s.rep.Sections[start:] {
		if sec.Error != "" {
			failures = append(failures, fmt.Errorf("№%d · %s: %s", sec.ID, sec.Title, sec.Error))
		}
		if key == "checkdb" && (sec.ID != 15 || len(sec.Rows) == 0) {
			failures = append(failures, errors.New("нет результата DBCC CHECKDB"))
		}
	}
	return errors.Join(failures...)
}

func (s *suite) skip(name, why string, ids ...int) error {
	s.rep.Steps = append(s.rep.Steps, SuiteStep{Started: time.Now(), Name: name, Status: statusSkipped, Detail: why})
	s.run.say(Event{Phase: name, Detail: why})
	for _, id := range ids {
		s.mark(id, statusSkipped, why)
	}
	return s.save()
}

// Keep an intermediate checkpoint and wall time for each actual operation.
// A step may fail independently; the caller decides which dependants to skip.
func (s *suite) step(name string, run func() error) error {
	if err := s.verify(s.ctx); err != nil {
		return err
	}
	i := len(s.rep.Steps)
	start := time.Now()
	s.rep.Steps = append(s.rep.Steps, SuiteStep{Started: start, Name: name, Status: "Выполняется"})
	if err := s.save(); err != nil {
		return err
	}
	s.run.say(Event{Phase: name, Detail: "Начато"})
	err := run()
	var omitted skippedStep
	skipped := errors.As(err, &omitted)
	if skipped {
		err = nil
	}
	err = errors.Join(err, s.verify(s.ctx))
	st := &s.rep.Steps[i]
	st.ElapsedMS = time.Since(start).Milliseconds()
	st.Status = statusDone
	if skipped {
		st.Status = statusSkipped
		st.Detail = omitted.reason
	}
	if err != nil {
		st.Status = statusFailed
		st.Detail = err.Error()
	}
	message := st.Status + " · " + time.Since(start).Round(time.Millisecond).String()
	if st.Detail != "" {
		message += " · " + st.Detail
	}
	s.run.say(Event{Phase: name, Detail: message})
	return errors.Join(err, s.save())
}
func (s *suite) interrupted(err error) error {
	var unsafe stopSuite
	if errors.As(err, &unsafe) {
		return err
	}
	return s.ctx.Err()
}
func (s *suite) nonfatal(name string, err error) error {
	if stop := s.interrupted(err); stop != nil {
		return stop
	}
	if err != nil {
		s.issues = append(s.issues, fmt.Errorf("%s: %w", name, err))
	}
	return nil
}
func (s *suite) inspect() error {
	err := s.step("Паспорт SQL", func() error {
		start := len(s.rep.Sections)
		err := s.work("inspect")
		for _, sec := range s.rep.Sections[start:] {
			if c := s.item(sec.ID); c != nil {
				c.ElapsedMS += sec.ElapsedMS
			}
		}
		return err
	})
	if stop := s.nonfatal("Паспорт SQL", err); stop != nil {
		return stop
	}
	if host, ok := s.run.Backend.(interface {
		StandHostSections(context.Context) []Section
	}); ok {
		err = s.step("Проверки ОС узла SQL", func() error {
			var failures []error
			data := false
			unavailable := ""
			for _, sec := range host.StandHostSections(s.ctx) {
				if sec.Error == "" {
					data = true
				} else if unavailable == "" {
					unavailable = sec.Error
				}
				if c := s.item(sec.ID); c != nil {
					c.ElapsedMS += sec.ElapsedMS
				}
				if e := s.record(sec); e != nil {
					return e
				}
				if sec.Error != "" && !strings.HasPrefix(sec.Error, "Нет данных ОС:") {
					failures = append(failures, errors.New(sec.Error))
				}
			}
			if !data && len(failures) == 0 && unavailable != "" {
				return skippedStep{unavailable}
			}
			return errors.Join(failures...)
		})
		if stop := s.nonfatal("Проверки ОС", err); stop != nil {
			return stop
		}
	}
	if host := s.run.Profile.NetworkProbeHost; host != "" {
		if probe, ok := s.run.Backend.(interface {
			ERPNetworkEvidence(context.Context, string) Section
		}); ok {
			err = s.step("Сеть от узла 1С до SQL", func() error {
				sec := probe.ERPNetworkEvidence(s.ctx, host)
				if c := s.item(31); c != nil {
					c.ElapsedMS += sec.ElapsedMS
				}
				if e := s.record(sec); e != nil {
					return e
				}
				if sec.Error != "" {
					return skippedStep{sec.Error}
				}
				return nil
			})
			if stop := s.nonfatal("Сеть от узла 1С до SQL", err); stop != nil {
				return stop
			}
		}
	}
	err = s.step("Сверка с утверждённым профилем", func() error {
		sec := compareBaseline(s.run.Root, s.run.Profile, *s.rep)
		if e := s.record(sec); e != nil {
			return e
		}
		if sec.Error != "" {
			s.mark(35, statusSkipped, sec.Error)
			if strings.HasPrefix(sec.Error, "Нет эталона") {
				return skippedStep{sec.Error}
			}
			return errors.New(sec.Error)
		}
		s.mark(35, statusDone, "Сверка с сохранённым эталоном, расхождения приведены в разделе №35.")
		return nil
	})
	return s.nonfatal("Сверка профиля", err)
}

func (s *suite) selectWorkload() error {
	p := s.run.Profile
	configured := len(p.Workload) > 0 || (p.ERPServer != "" && p.ERPDatabase != "" && uuid.MatchString(p.DocumentUUID) && p.Document != "")
	erpWhy := "В профиле не заданы команда Workload или подключение 1С с документом и UUID."
	if len(s.run.ERPDiagnostics) > 0 && !configured {
		erpWhy = s.run.ERPDiagnostics[len(s.run.ERPDiagnostics)-1]
	}
	if configured || s.run.Demo {
		s.source = "1С · сценарий профиля"
		if len(p.Workload) == 0 {
			s.source = "1С · чтение одного документа · " + p.Document
		}
		if s.run.Demo {
			s.source = "Демо · синтетические данные"
		}
		s.rep.Workload = s.source
		s.sessions = p.Sessions
		err := s.step(suiteBaseline, func() error { return s.measure(s.ctx, suiteBaseline, nil) })
		if err == nil {
			s.ready, s.usingERP = true, true
			s.baseline = suiteBaseline
			return s.scaling()
		}
		if stop := s.nonfatal("Исходный замер сценария", err); stop != nil {
			return stop
		}
		erpWhy = err.Error()
	} else if err := s.skip("Нагрузка 1С", erpWhy); err != nil {
		return err
	}
	s.mark(11, statusSkipped, erpWhy)
	s.mark(ClusterID, statusSkipped, "Для изменения кластера требуется работающий сценарий 1С. "+erpWhy)
	if s.run.RequireERP {
		s.rep.Workload = "1С · исходный замер не получен"
		return fmt.Errorf("Прогон через 1С остановлен до изменения настроек: %s", erpWhy)
	}
	sql, ok := s.run.Backend.(interface {
		MeasureSQL(context.Context) (map[string]float64, error)
	})
	if !ok {
		s.rep.Workload = "Нет доступной нагрузки"
		return s.skip("Контрольная нагрузка SQL", "SQL-сценарий недоступен.")
	}
	s.source = "SQL · чтение тестовой базы, один сеанс"
	s.rep.Workload = s.source
	s.rep.Journal = append(s.rep.Journal, "SQL-сценарий измеряет время чтения одних и тех же ограниченных диапазонов индексов со стороны клиента SQL. Время пользовательских операций 1С не измеряется. Параллельный план и пересчёт статистики зависят от данных и условий прогона.")
	s.sessions = 1
	s.load = sql.MeasureSQL
	if t, ok := s.run.Backend.(interface {
		SQLSnapshot(context.Context) Counters
	}); ok {
		s.snapshot = t.SQLSnapshot
	}
	s.reference = nil
	const name = "Исходный замер SQL"
	err := s.step(name, func() error { return s.measure(s.ctx, name, nil) })
	if err != nil {
		return s.nonfatal(name, err)
	}
	s.ready = true
	s.baseline = name
	return nil
}

func (s *suite) scaling() error {
	p := s.run.Profile
	m, ok := s.run.Backend.(interface {
		MeasureSessions(context.Context, int) (map[string]float64, error)
	})
	if p.Sessions < 2 || !ok {
		return s.skip("Рост нагрузки 1С", "Нужен сценарий с несколькими сеансами и Sessions больше 1.", 11)
	}
	// Both series use the original settings. The configured-concurrency stage
	// has already run; compare a fresh series at 1 worker to it explicitly.
	start := time.Now()
	defer func() { s.item(11).ElapsedMS += time.Since(start).Milliseconds() }()
	multi := s.baseline
	s.load = func(ctx context.Context) (map[string]float64, error) { return m.MeasureSessions(ctx, 1) }
	s.sessions = 1
	const one = "Один сеанс 1С"
	err := s.step(one, func() error { return s.measure(s.ctx, one, []int{11}) })
	s.sessions = p.Sessions
	s.load = s.run.Backend.Measure
	if err == nil {
		s.baseline = one
		name := fmt.Sprintf("Нагрузка 1С · %d сеансов", p.Sessions)
		err = s.step(name, func() error { return s.measure(s.ctx, name, []int{11}) })
	}
	s.baseline = multi
	if err != nil {
		s.mark(11, statusFailed, err.Error())
		return s.nonfatal("Рост нагрузки", err)
	}
	s.mark(11, statusMeasured, fmt.Sprintf("Измерено время операций при 1 и %d сеансах сценария.", p.Sessions))
	return nil
}

func (s *suite) reversible() error {
	ids := []int{18, 19, 23, 24, 25, 28, 34, ClusterID}
	for _, id := range ids {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		if id == ClusterID && !s.usingERP {
			continue
		}
		name := ByID(id).Title
		if !s.ready {
			if err := s.skip(name, "Нет успешной исходной серии для сравнения.", id); err != nil {
				return err
			}
			continue
		}
		if id == ClusterID && !s.run.Demo && s.run.Profile.Cluster.RAC == "" {
			if err := s.skip(name, "В профиле не настроен rac для выделенного рабочего сервера 1С.", id); err != nil {
				return err
			}
			continue
		}
		if err := s.verify(s.ctx); err != nil {
			return err
		}
		captureStart := time.Now()
		captureErr := s.capture(s.ctx, id)
		elapsed := time.Since(captureStart).Milliseconds()
		s.item(id).ElapsedMS += elapsed
		if err := captureErr; err != nil {
			var omitted skippedStep
			if errors.As(err, &omitted) {
				if e := s.skip(name, err.Error(), id); e != nil {
					return e
				}
				if instanceOption(id) != "" && len(s.item(id).Evidence) > 0 {
					s.mark(id, statusEvidence, err.Error())
				}
			} else {
				s.mark(id, statusFailed, err.Error())
				s.rep.Steps = append(s.rep.Steps, SuiteStep{Started: captureStart, Name: name, Status: statusFailed, Detail: err.Error(), ElapsedMS: elapsed})
				s.run.say(Event{Phase: name, Detail: err.Error()})
				if stop := s.nonfatal(name, err); stop != nil {
					return stop
				}
				if e := s.save(); e != nil {
					return e
				}
			}
			continue
		}
		if s.rep.Before[id] == s.rep.Targets[id] {
			msg := fmt.Sprintf("Текущее значение %s совпадает с целью. Отдельного изменения и сравнения нет.", settingText(id, s.rep.Before[id]))
			if err := s.skip(name, msg); err != nil {
				return err
			}
			if id == 19 {
				msg += " Так проверяется MAXDOP базы. Для порога стоимости экземпляра предусмотрен только сбор показаний."
			}
			s.mark(id, statusSame, msg)
			continue
		}
		start := time.Now()
		err := s.step(name, func() error { return s.trial(s.ctx, name, []int{id}) })
		s.item(id).ElapsedMS += time.Since(start).Milliseconds()
		if err != nil {
			s.mark(id, statusFailed, err.Error())
			if stop := s.nonfatal(name, err); stop != nil {
				return stop
			}
			continue
		}
		msg := fmt.Sprintf("%s → %s. Исходное значение возвращено и прочитано повторно. Нагрузка: %s.", settingText(id, s.rep.Before[id]), settingText(id, s.rep.Targets[id]), s.source)
		switch id {
		case 19:
			msg += " Изменён MAXDOP выбранной базы. Cost threshold for parallelism экземпляра только снят в паспорте."
		case 23:
			msg += " CHECKSUM записывается при записи страниц. Замер чтения не проверяет затраты на запись."
		case 28:
			msg += " Это настройка экземпляра. Возврат лимита не прогревает заново вытесненные страницы."
		case 34:
			msg += " Изменение действует для новых компиляций. Кэш планов не очищался."
		}
		if id == 18 {
			msg += " История Query Store сохраняется."
		}
		s.mark(id, statusControl, msg)
		s.tested = append(s.tested, id)
	}
	if len(s.tested) > 1 {
		const name = "Все доступные настройки вместе"
		err := s.step(name, func() error { return s.trial(s.ctx, name, s.tested) })
		if stop := s.nonfatal(name, err); stop != nil {
			return stop
		}
	}
	if len(s.tested) > 0 {
		const name = "Контроль после отката"
		err := s.step(name, func() error { return s.measure(s.ctx, name, nil) })
		if stop := s.nonfatal(name, err); stop != nil {
			return stop
		}
	}
	reason := "Проверены идентификатор стенда и журнал незавершённых изменений."
	if len(s.tested) > 0 {
		reason += " Настройки отдельной серии и результат возврата записаны в журнале."
	}
	s.mark(13, statusDone, reason)
	return nil
}

func (s *suite) integrity() error {
	const name = "Проверка целостности базы"
	start := time.Now()
	err := s.step(name, func() error { return s.work("checkdb") })
	s.item(15).ElapsedMS += time.Since(start).Milliseconds()
	if err != nil {
		s.mark(15, statusFailed, err.Error())
		return fmt.Errorf("проверка целостности не завершилась успешно. Обслуживание и копирование остановлены: %w", err)
	}
	s.mark(15, statusDone, "DBCC CHECKDB выполнен на выбранной базе. Для длительности восстановления нужен протокол самой операции RESTORE.")
	return nil
}

func (s *suite) maintenance() error {
	for _, job := range []struct {
		id                int
		key, name, result string
	}{
		{3, "stats", "обновления статистики", "Обновлено"},
		{32, "indexes", "обслуживания индексов", "Обслужено"},
	} {
		start := time.Now()
		before := "До " + job.name
		baselineOK := false
		originalBaseline := s.baseline
		if s.ready {
			err := s.step(before, func() error { return s.measure(s.ctx, before, nil) })
			if stop := s.nonfatal(before, err); stop != nil {
				return stop
			}
			baselineOK = err == nil
		}
		first := len(s.rep.Sections)
		j, _ := FindJob(job.key)
		err := s.step(j.Name, func() error { return s.work(job.key) })
		changed := maintenanceChanged(s.rep.Sections[first:], job.result)
		if changed {
			s.maintained = append(s.maintained, job.id)
		}
		c := s.item(job.id)
		c.ElapsedMS += time.Since(start).Milliseconds()
		if err != nil {
			msg := err.Error()
			if changed {
				msg += " Часть изменений уже применена и остаётся в копии."
			}
			s.mark(job.id, statusFailed, msg)
			if stop := s.nonfatal(j.Name, err); stop != nil {
				return stop
			}
			continue
		}
		if !changed {
			s.mark(job.id, statusDone, "Подходящих кандидатов на момент выполнения нет. Изменений и парного замера нет.")
			continue
		}
		if !baselineOK {
			s.mark(job.id, statusDone, "Обслуживание выполнено один раз, изменения сохраняются в копии. Замер эффекта недоступен.")
			continue
		}
		s.baseline = before
		after := "После " + job.name
		start = time.Now()
		err = s.step(after, func() error { return s.measure(s.ctx, after, []int{job.id}) })
		s.baseline = originalBaseline
		c.ElapsedMS += time.Since(start).Milliseconds()
		if err != nil {
			s.mark(job.id, statusFailed, "Изменение выполнено и сохраняется в копии. Ошибка контрольного замера: "+err.Error())
			if stop := s.nonfatal(after, err); stop != nil {
				return stop
			}
		} else {
			s.mark(job.id, statusMeasured, "Выполнено обслуживание и замер до и после на одной нагрузке. Изменение сохраняется в копии. Нагрузка: "+s.source+".")
		}
	}
	return nil
}

func (s *suite) finishMeasurements() error {
	if err := s.finalCombined(); err != nil {
		return err
	}
	reader, ok := s.run.Backend.(interface{ BlockingEvidence(context.Context) Section })
	if !ok || !s.ready {
		return nil
	}
	const name = "Блокировки после нагрузки"
	err := s.step(name, func() error {
		sec := reader.BlockingEvidence(s.ctx)
		sec.Title += " · после нагрузки"
		s.item(22).ElapsedMS += sec.ElapsedMS
		if err := s.record(sec); err != nil {
			return err
		}
		if sec.Error != "" {
			return errors.New(sec.Error)
		}
		s.mark(22, statusEvidence, "Собраны данные о блокировках после нагрузочных серий. Сопоставь время событий с границами серий и технологическим журналом 1С.")
		return nil
	})
	return s.nonfatal(name, err)
}

func (s *suite) finalCombined() error {
	if !s.ready || len(s.maintained) == 0 {
		return nil
	}
	const name = "Итог после обслуживания и совместной настройки"
	err := s.step(name, func() error {
		if len(s.tested) > 0 {
			return s.trial(s.ctx, name, s.tested)
		}
		return s.measure(s.ctx, name, nil)
	})
	if err == nil {
		ph := &s.rep.Phases[len(s.rep.Phases)-1]
		ph.IDs = append(ph.IDs, s.maintained...)
		s.rep.Journal = append(s.rep.Journal, "Итоговая серия включает изменения обслуживания, которые остаются в копии. Она показывает общий результат, без разделения вклада отдельных изменений.")
	}
	return s.nonfatal(name, err)
}
func (s *suite) backup() error {
	const name = "Сравнение и проверка копий"
	if !serverAbs(strings.TrimSpace(s.run.Profile.BackupDir)) {
		return s.skip(name, "Укажи абсолютную папку BackupDir на стороне SQL с местом для двух полных копий.", 14)
	}
	start := time.Now()
	err := s.step(name, func() error { return s.work("backup") })
	s.item(14).ElapsedMS += time.Since(start).Milliseconds()
	if err != nil {
		s.mark(14, statusFailed, err.Error())
		return s.nonfatal(name, err)
	}
	s.mark(14, statusMeasured, "Созданы и проверены две отдельные COPY_ONLY копии с CHECKSUM. Размеры и время — в разделах №14. Файлы остаются в папке SQL.")
	return nil
}
