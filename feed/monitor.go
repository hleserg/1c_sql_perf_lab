package feed

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"
)

var monitorProfileKey = regexp.MustCompile(`^[0-9a-f]{24}$`)

// QueueMonitorDay gives a finalized UTC day a stable delivery path. Requeueing
// identical content after a crash uses the same immutable report in GitHub.
func QueueMonitorDay(root string, cfg Config, profileKey, day string, report any) error {
	if !monitorProfileKey.MatchString(profileKey) {
		return errors.New("неверный ключ профиля мониторинга")
	}
	if _, err := time.Parse(time.DateOnly, day); err != nil {
		return err
	}
	id := fmt.Sprintf("monitor-%s-%s", profileKey, day)
	dir := baseDir(root, cfg)
	s := Session{Root: root, Config: cfg, ID: id, dir: filepath.Join(dir, id)}
	return s.Report(report)
}
