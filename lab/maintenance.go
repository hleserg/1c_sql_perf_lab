package lab

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Both the report and the updater use this exact candidate list. Exclude filtered,
// incremental and NORECOMPUTE statistics as well as disabled and hypothetical indexes.
const statsList = `SELECT TOP (20) st.object_id AS [Object ID],st.stats_id AS [Stats ID],sc.name AS [Схема],t.name AS [Таблица],st.name AS [Статистика],
 p.rows AS [Строк],p.modification_counter AS [Изменений],p.last_updated AS [Последнее обновление]
 FROM sys.stats st JOIN sys.tables t ON t.object_id=st.object_id
 JOIN sys.schemas sc ON sc.schema_id=t.schema_id
 LEFT JOIN sys.indexes i ON i.object_id=st.object_id AND i.index_id=st.stats_id
 CROSS APPLY sys.dm_db_stats_properties(st.object_id,st.stats_id) p
 WHERE t.is_ms_shipped=0 AND t.is_memory_optimized=0 AND st.has_filter=0 AND st.no_recompute=0 AND st.is_incremental=0 AND st.is_temporary=0
 AND (i.index_id IS NULL OR (i.is_disabled=0 AND i.is_hypothetical=0))
 AND p.rows>=10000 AND p.modification_counter>=1000 AND p.modification_counter>=p.rows*0.10
 ORDER BY p.modification_counter DESC,st.object_id,st.stats_id`

func (l *Live) updateStats(ctx context.Context, save func(Section) error, note func(string)) error {
	note("Поиск наиболее изменившихся статистик")
	list := fetchSection(ctx, l.conn, check{3, "Кандидаты до обновления", statsList})
	if e := save(list); e != nil {
		return e
	}
	if list.Error != "" {
		return errors.New(list.Error)
	}
	for _, r := range list.Rows {
		if e := ctx.Err(); e != nil {
			return e
		}
		title := r[2] + "." + r[3] + " · " + r[4]
		note(title)
		// Resolve the trusted catalog identifiers again and recheck the threshold at execution.
		const update = `DECLARE @statement nvarchar(max);
 SELECT @statement=N'UPDATE STATISTICS '+QUOTENAME(sc.name)+N'.'+QUOTENAME(t.name)+N' '+QUOTENAME(st.name)
 FROM sys.stats st JOIN sys.tables t ON t.object_id=st.object_id JOIN sys.schemas sc ON sc.schema_id=t.schema_id
 LEFT JOIN sys.indexes i ON i.index_id=st.stats_id AND i.object_id=st.object_id
 CROSS APPLY sys.dm_db_stats_properties(st.object_id,st.stats_id) p
 WHERE st.object_id=CONVERT(int,@p1) AND st.stats_id=CONVERT(int,@p2) AND sc.name=@p3 AND t.name=@p4 AND st.name=@p5
 AND t.is_ms_shipped=0 AND t.is_memory_optimized=0 AND st.no_recompute=0 AND st.has_filter=0 AND st.is_incremental=0 AND st.is_temporary=0
 AND (i.index_id IS NULL OR (i.is_disabled=0 AND i.is_hypothetical=0))
 AND p.rows>=10000 AND p.modification_counter>=1000 AND p.modification_counter>=p.rows*0.10;
 IF @statement IS NOT NULL EXEC sys.sp_executesql @statement;
 SELECT CASE WHEN @statement IS NULL THEN N'Пропущено после перепроверки' ELSE N'Обновлено' END AS [Результат],
 (SELECT modification_counter FROM sys.dm_db_stats_properties(CONVERT(int,@p1),CONVERT(int,@p2))) AS [Изменений после]`
		s := fetchSection(ctx, l.conn, check{3, title, update}, r[0], r[1], r[2], r[3], r[4])
		if e := save(s); e != nil {
			return e
		}
		if s.Error != "" {
			return errors.New(s.Error)
		}
	}
	return nil
}
func (l *Live) backupTrial(ctx context.Context, save func(Section) error, note func(string)) error {
	dir := strings.TrimSpace(l.P.BackupDir)
	if !serverAbs(dir) {
		return errors.New("укажи абсолютную папку BackupDir на стороне SQL с местом для двух полных копий")
	}
	var id [16]byte
	if _, e := rand.Read(id[:]); e != nil {
		return e
	}
	sep := "\\"
	if strings.HasPrefix(dir, "/") {
		sep = "/"
	}
	prefix := strings.TrimRight(dir, "/\\") + sep + "perflab-" + hex.EncodeToString(id[:])
	for _, mode := range []string{"NO_COMPRESSION", "COMPRESSION"} {
		path := prefix + "-" + mode + ".bak"
		note("BACKUP · " + mode)
		s := Section{ID: 14, Title: mode, Columns: []string{"Файл SQL", "Байты копии", "Байты на носителе", "BACKUP мс", "VERIFYONLY мс", "Проверка"}}
		// Persist the new path before issuing BACKUP so an interrupted backup is identifiable.
		if e := save(Section{ID: 14, Title: "Назначение · " + mode, Columns: []string{"Новый файл SQL"}, Rows: [][]string{{path}}}); e != nil {
			return e
		}
		start := time.Now()
		_, e := l.conn.ExecContext(ctx, `DECLARE @db sysname=DB_NAME(); BACKUP DATABASE @db TO DISK=@p1 WITH COPY_ONLY,CHECKSUM,`+mode, path)
		backupMS := time.Since(start).Milliseconds()
		var verifyMS, raw, compressed int64
		status := "Не проверена"
		if e == nil {
			note("RESTORE VERIFYONLY · " + mode)
			start = time.Now()
			_, e = l.conn.ExecContext(ctx, `RESTORE VERIFYONLY FROM DISK=@p1 WITH FILE=1,CHECKSUM`, path)
			verifyMS = time.Since(start).Milliseconds()
			if e == nil {
				status = "VERIFYONLY успешно"
				e = l.conn.QueryRowContext(ctx, `SELECT TOP(1) CONVERT(bigint,b.backup_size),CONVERT(bigint,b.compressed_backup_size) FROM msdb.dbo.backupset b JOIN msdb.dbo.backupmediafamily m ON m.media_set_id=b.media_set_id WHERE m.physical_device_name=@p1 AND b.database_name=DB_NAME() AND b.is_copy_only=1 AND b.has_backup_checksums=1 ORDER BY b.backup_set_id DESC`, path).Scan(&raw, &compressed)
			}
		}
		s.Rows = [][]string{{path, fmt.Sprint(raw), fmt.Sprint(compressed), fmt.Sprint(backupMS), fmt.Sprint(verifyMS), status}}
		s.ElapsedMS = backupMS + verifyMS
		if e != nil {
			s.Error = e.Error()
		}
		if err := save(s); err != nil {
			return err
		}
		if e != nil {
			return e
		}
	}
	return nil
}
func serverAbs(p string) bool {
	return (strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || len(p) > 2 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':' && (p[2] == '\\' || p[2] == '/')) && !strings.ContainsRune(p, 0)
}
