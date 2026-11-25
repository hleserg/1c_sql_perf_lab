package lab

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Cluster struct {
	RAC         string
	Endpoint    string
	ClusterID   string
	ServerID    string
	Connections int
}
type Profile struct {
	RestoreDir                                        string `json:",omitempty"`
	BackupDir                                         string
	Name, SQLHost, Instance, Database, ExpectedServer string
	TrustServerCertificate                            bool
	MaxDOP                                            int
	OptimizerBuild                                    string
	AsyncStats                                        bool
	Samples, Warmup, Sessions, TimeoutSeconds         int
	Workload                                          []string
	ERPServer, ERPDatabase, Document, DocumentUUID    string
	Cluster                                           Cluster
}

func Example() Profile {
	return Profile{Name: "Стенд ERP", SQLHost: "localhost", Database: "erp_lab", ExpectedServer: "LAB-SQL", MaxDOP: 1, AsyncStats: true, Samples: 5, Warmup: 1, Sessions: 1, TimeoutSeconds: 120, ERPServer: "localhost", ERPDatabase: "erp_lab", Document: "ЗаказКлиента"}
}
func Load(path string) (Profile, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return Profile{}, e
	}
	var p Profile
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&p); e != nil {
		return p, e
	}
	return p, p.Validate()
}
func (p Profile) Validate() error {
	if p.SQLHost == "" || p.Database == "" || p.ExpectedServer == "" {
		return errors.New("заполни SQLHost, Database и ExpectedServer в профиле")
	}
	if p.Samples < 3 || p.Samples > 100 || p.Warmup < 1 || p.Warmup > 20 || p.Sessions < 1 || p.Sessions > 64 || p.TimeoutSeconds < 5 || p.TimeoutSeconds > 3600 || p.MaxDOP < 1 || p.MaxDOP > 64 {
		return errors.New("проверь Samples (3..100), Warmup (1..20), Sessions (1..64), TimeoutSeconds (5..3600), MaxDOP (1..64)")
	}
	return nil
}
func (p Profile) Timeout() time.Duration { return time.Duration(p.TimeoutSeconds) * time.Second }
func (p Profile) Target(id int) int {
	switch id {
	case 19:
		return p.MaxDOP
	case 24:
		return 1
	case 25:
		if p.AsyncStats {
			return 1
		}
		return 0
	case ClusterID:
		return p.Cluster.Connections
	}
	return 0
}
func (p Profile) Key() string {
	h := sha256.Sum256([]byte(strings.ToUpper(p.ExpectedServer + "|" + p.Database)))
	return fmt.Sprintf("%x", h[:12])
}
func SaveJSON(path string, value any) error {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	dir := filepath.Dir(path)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".lab-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(append(b, '\n')); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
