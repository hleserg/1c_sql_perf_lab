package lab

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepairUsesOnlyInstalledRunningAgent(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "1cv8", "8.3.27.1000", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ragent.exe", "comcntr.dll"} {
		if err := os.WriteFile(filepath.Join(bin, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	procs := []platformProcess{{"ragent.exe", filepath.Join(bin, "ragent.exe"), []int{2540}}}
	got, port, err := installedBin(root, procs)
	if err != nil || port != 2540 {
		t.Fatal(got, port, err)
	}
	if _, err := platformFile(got, "comcntr.dll"); err != nil {
		t.Fatal(err)
	}
	if _, err := platformFile(got, "absent.dll"); err == nil {
		t.Fatal("missing component")
	}
	if _, _, err := installedBin(root, append(procs, procs...)); err == nil {
		t.Fatal("ambiguous agent")
	}
	if _, _, err := installedBin(t.TempDir(), procs); err == nil {
		t.Fatal("untrusted directory")
	}
}

func TestRepairFailurePersistsBeforeSQL(t *testing.T) {
	r, stand := newSuiteRun(t)
	r.ERPPrepareError = errors.New("подробности подготовки")
	rep, path, err := r.Execute(context.Background(), nil)
	if err == nil || rep.Error == "" || !strings.Contains(rep.Error, r.ERPPrepareError.Error()) || len(stand.jobs) > 0 || len(stand.touched) > 0 || rep.Workload != "1С · исходный замер не получен" {
		t.Fatal(rep.Error, stand.jobs)
	}
	for _, name := range []string{path, filepath.Join(filepath.Dir(path), "report.html")} {
		b, err := os.ReadFile(name)
		if err != nil || !strings.Contains(string(b), r.ERPPrepareError.Error()) {
			t.Fatal(name, err)
		}
	}
	if !strings.Contains(platformFailure([]byte("HRESULT 0x8002801D; Pwd=secret")), "библиотека типов COM") {
		t.Fatal("unrecognized type library error")
	}
}

// A separate process is needed to exercise job-object ownership on Windows.
// The fixture accepts precisely the public RAS and RAC argument shapes we use.
func init() {
	if os.Getenv("PERFLAB_TEST_TEMPORARY_RAS") != "child" {
		return
	}
	args := os.Args[1:]
	switch filepath.Base(os.Args[0]) {
	case "fixture-ras.exe":
		if len(args) != 3 || args[0] != "cluster" || !strings.HasPrefix(args[1], "--port=") || args[2] != "127.0.0.1:2540" {
			os.Exit(2)
		}
		ln, err := net.Listen("tcp4", "127.0.0.1:"+strings.TrimPrefix(args[1], "--port="))
		if err != nil {
			os.Exit(3)
		}
		for {
			c, err := ln.Accept()
			if err != nil {
				os.Exit(4)
			}
			c.Close()
		}
	case "fixture-rac.exe":
		if len(args) != 3 || args[0] != "cluster" || args[1] != "list" {
			os.Exit(5)
		}
		c, err := net.DialTimeout("tcp", args[2], time.Second)
		if err != nil {
			os.Exit(6)
		}
		c.Close()
		os.Exit(0)
	}
}

func TestTemporaryRASLivesThroughCancelThenCloses(t *testing.T) {
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	ras, rac := filepath.Join(bin, "fixture-ras.exe"), filepath.Join(bin, "fixture-rac.exe")
	for _, name := range []string{ras, rac} {
		if err := os.WriteFile(name, data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	free, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := free.Addr().String()
	free.Close()
	t.Setenv("PERFLAB_TEST_TEMPORARY_RAS", "child")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, err := temporaryRAS(ctx, ras, Cluster{RAC: rac, Endpoint: endpoint}, 2540)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if second, err := temporaryRAS(ctx, ras, Cluster{RAC: rac, Endpoint: endpoint}, 2540); err == nil {
		second()
		t.Fatal("claimed existing listener")
	}
	cancel()
	c, err := net.DialTimeout("tcp", endpoint, time.Second)
	if err != nil {
		t.Fatal("RAS unavailable during rollback", err)
	}
	c.Close()
	release()
	if c, err = net.DialTimeout("tcp", endpoint, time.Second); err == nil {
		c.Close()
		t.Fatal("temporary server left running")
	}
}
