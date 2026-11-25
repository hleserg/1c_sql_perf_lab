package lab

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Counters struct {
	At                     time.Time
	Files                  map[string]FileIO
	Processes, Connections int
	WorkingMemoryKB        int64
	Notes                  []string
}
type FileIO struct{ Reads, Writes, ReadWaitMS, WriteWaitMS int64 }
type Telemetry interface {
	Snapshot(context.Context) Counters
}

func (l *Live) Snapshot(ctx context.Context) Counters {
	c := Counters{At: time.Now(), Files: map[string]FileIO{}}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, e := l.conn.QueryContext(bounded, `SELECT f.name,v.num_of_reads,v.num_of_writes,v.io_stall_read_ms,v.io_stall_write_ms FROM sys.dm_io_virtual_file_stats(DB_ID(),NULL) v JOIN sys.database_files f ON f.file_id=v.file_id`)
	if e != nil {
		c.Notes = append(c.Notes, "Счётчики I/O недоступны: "+e.Error())
	} else {
		for rows.Next() {
			var name string
			var io FileIO
			if e = rows.Scan(&name, &io.Reads, &io.Writes, &io.ReadWaitMS, &io.WriteWaitMS); e != nil {
				break
			}
			c.Files[name] = io
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			c.Notes = append(c.Notes, e.Error())
		}
	}
	rc := l.P.Cluster
	if rc.RAC != "" {
		out, e := exec.CommandContext(bounded, rc.RAC, "process", "list", "--cluster="+rc.ClusterID, rc.Endpoint).CombinedOutput()
		if e != nil {
			c.Notes = append(c.Notes, "Список rphost недоступен: "+e.Error())
		} else {
			for _, rec := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n\n") {
				fields := map[string]string{}
				for _, line := range strings.Split(rec, "\n") {
					k, v, ok := strings.Cut(line, ":")
					if ok {
						fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
					}
				}
				if fields["process"] == "" {
					continue
				}
				c.Processes++
				n, _ := strconv.Atoi(fields["connections"])
				c.Connections += n
				ram, _ := strconv.ParseInt(fields["memory-size"], 10, 64)
				c.WorkingMemoryKB += ram
			}
		}
	}
	return c
}
func (l *Live) extras(ctx context.Context, facts map[string]string) {
	queries := []struct{ k, q string }{
		{"Уровень совместимости", "SELECT compatibility_level FROM sys.databases WHERE database_id=DB_ID()"},
		{"Защита страниц", "SELECT page_verify_option_desc FROM sys.databases WHERE database_id=DB_ID()"},
		{"AUTO_CLOSE / AUTO_SHRINK", "SELECT CONCAT(CONVERT(int,is_auto_close_on),' / ',CONVERT(int,is_auto_shrink_on)) FROM sys.databases WHERE database_id=DB_ID()"},
		{"Модель восстановления", "SELECT recovery_model_desc FROM sys.databases WHERE database_id=DB_ID()"},
		{"Файлов данных tempdb", "SELECT COUNT(*) FROM tempdb.sys.database_files WHERE type=0"},
		{"CPU / память узла SQL, МБ", "SELECT CONCAT(cpu_count,' / ',physical_memory_kb/1024) FROM sys.dm_os_sys_info"},
		{"Лимит памяти SQL, МБ", "SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name='max server memory (MB)'"},
		{"Состояние Query Store", "SELECT actual_state_desc FROM sys.database_query_store_options"},
		{"msdb · последнее полное копирование", "SELECT COALESCE(CONVERT(varchar(23),MAX(backup_finish_date),121),'нет в истории') FROM msdb.dbo.backupset WHERE database_name=DB_NAME() AND type='D'"},
	}
	for _, x := range queries {
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		var v string
		e := l.conn.QueryRowContext(bounded, x.q).Scan(&v)
		cancel()
		if e != nil {
			facts[x.k] = "недоступно"
		} else {
			facts[x.k] = v
		}
	}
}
func (f FileIO) String() string { return fmt.Sprintf("%d / %d", f.Reads, f.Writes) }
