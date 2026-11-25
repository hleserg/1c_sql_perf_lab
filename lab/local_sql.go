package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// LocalBackups enumerates directory entries without opening the backup payloads.
func LocalBackups(dir string) ([]BackupFile, error) {
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("прочитать папку на этом компьютере: %w", err)
	}
	var files []BackupFile
	for _, entry := range list {
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".bak") {
			continue
		}
		info, err := entry.Info()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			files = append(files, BackupFile{filepath.Join(dir, entry.Name()), info.ModTime()})
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Modified.After(files[j].Modified) })
	return files, nil
}

type sqlInstance struct{ name, port string }
type LocalTarget struct {
	Profile          Profile
	Login            string
	New, SourceMatch bool
}

func windowsUser() string {
	if u, e := user.Current(); e == nil {
		return u.Username
	}
	return os.Getenv("USERNAME")
}

// Only instances registered on this machine and loopback endpoints are probed.
func DiscoverLocalSQL(ctx context.Context, base Profile, backup string) ([]LocalTarget, error) {
	instances := localInstances()
	type result struct {
		targets []LocalTarget
		err     error
	}
	results := make(chan result, len(instances))
	var group sync.WaitGroup
	for _, instance := range instances {
		group.Add(1)
		go func() {
			defer group.Done()
			targets, err := probeInstance(ctx, base, instance, backup)
			results <- result{targets, err}
		}()
	}
	group.Wait()
	close(results)
	var all []LocalTarget
	var failed []error
	for r := range results {
		all = append(all, r.targets...)
		if r.err != nil {
			failed = append(failed, r.err)
		}
	}
	if len(all) == 0 {
		if len(failed) > 0 {
			return nil, errors.Join(failed...)
		}
		return nil, errors.New("На экземпляре нет доступных пользовательских баз. Открой резервную копию для создания базы.")
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.SourceMatch != b.SourceMatch {
			return a.SourceMatch
		}
		if a.Profile.ExpectedServer != b.Profile.ExpectedServer {
			return a.Profile.ExpectedServer < b.Profile.ExpectedServer
		}
		return a.Profile.Database < b.Profile.Database
	})
	return all, nil
}

func probeInstance(ctx context.Context, base Profile, instance sqlInstance, backup string) ([]LocalTarget, error) {
	base.SQLHost = "localhost"
	base.Instance = instance.name
	base.TrustServerCertificate = true // local Windows connection, including developer certificates
	var tries []Profile
	if localSharedMemory {
		p := base
		p.SQLProtocol = "lpc"
		tries = append(tries, p)
	}
	p := base
	p.SQLProtocol = "tcp"
	if instance.port != "" {
		p.SQLHost = "127.0.0.1:" + instance.port
		p.Instance = ""
	}
	tries = append(tries, p)
	var failures []error
	for _, candidate := range tries {
		l := &Live{P: candidate}
		connect, stop := context.WithTimeout(ctx, 5*time.Second)
		err := l.connect(connect, "master")
		stop()
		if err != nil {
			l.Close()
			failures = append(failures, fmt.Errorf("%s: %w", candidate.SQLProtocol, err))
			continue
		}
		targets, err := localTargets(ctx, l.conn, candidate, instance.name, backup)
		l.Close()
		return targets, err
	}
	label := "localhost"
	if instance.name != "" {
		label += "\\" + instance.name
	}
	return nil, fmt.Errorf("подключение к %s от %s: %w", label, windowsUser(), errors.Join(failures...))
}

const protectedDatabase = "database_id<=4 OR is_distributor=1 OR is_published=1 OR is_subscribed=1 OR is_merge_published=1 OR source_database_id IS NOT NULL OR replica_id IS NOT NULL OR EXISTS(SELECT 1 FROM sys.databases s WHERE s.source_database_id=d.database_id)"

func localTargets(ctx context.Context, c *sql.Conn, p Profile, instance, backup string) ([]LocalTarget, error) {
	var login, name string
	if err := c.QueryRowContext(ctx, "SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')),COALESCE(CONVERT(nvarchar(128),SERVERPROPERTY('InstanceName')),N''),ORIGINAL_LOGIN()").Scan(&p.ExpectedServer, &name, &login); err != nil {
		return nil, err
	}
	if !strings.EqualFold(name, instance) {
		return nil, fmt.Errorf("ожидался экземпляр %q, SQL сообщил %q", instance, name)
	}
	source := ""
	if backup != "" {
		rows, err := metadata(ctx, c, "RESTORE HEADERONLY FROM DISK=@p1", backup)
		if err == nil {
			var h metaRow
			h, err = fullSet(rows)
			source = h.get("DatabaseName")
		}
		if err != nil {
			return nil, fmt.Errorf("%s, вход %s, чтение заголовка бэкапа: %w", p.ExpectedServer, login, err)
		}
	}
	rows, err := c.QueryContext(ctx, "SELECT name FROM sys.databases d WHERE NOT ("+protectedDatabase+") AND state_desc IN ('ONLINE','OFFLINE','RESTORING') ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("%s, вход %s, список баз: %w", p.ExpectedServer, login, err)
	}
	var targets []LocalTarget
	for rows.Next() {
		var db string
		if err := rows.Scan(&db); err != nil {
			rows.Close()
			return nil, err
		}
		if safeTarget(db) {
			p.Database = db
			targets = append(targets, LocalTarget{p, login, false, strings.EqualFold(db, source)})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if source != "" && safeTarget(source) {
		var count int
		if err = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.databases WHERE name=@p1", source).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			p.Database = source
			targets = append(targets, LocalTarget{p, login, true, true})
		}
	}
	return targets, nil
}
