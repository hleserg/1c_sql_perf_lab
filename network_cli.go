package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func networkProbeCLI(args []string) error {
	f := flag.NewFlagSet("network-probe", flag.ContinueOnError)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	root := f.String("home", filepath.Dir(exe), "Каталог с local/stand.json")
	expected := f.String("erp-host", "", "Имя рабочего Windows-узла 1С, на котором запущена лаборатория")
	if err = f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || strings.TrimSpace(*expected) == "" {
		return errors.New("задай имя рабочего узла 1С через -erp-host")
	}
	p, err := lab.Load(profilePath(*root))
	if err != nil {
		return err
	}
	base := filepath.Join(*root, "runs")
	if err = os.MkdirAll(base, 0700); err != nil {
		return err
	}
	start := time.Now()
	dir, err := os.MkdirTemp(base, start.Format("20060102-150405")+"-network-")
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	return deliverCommand(*root, "network-probe", func(progress func(string)) (string, error) {
		progress("Замер соединений от узла 1С до SQL")
		l := &lab.Live{P: p}
		s := l.ERPNetworkEvidence(ctx, *expected)
		report := lab.Report{Started: start, Finished: time.Now(), Job: s.Title, Samples: lab.ERPNetworkSamples, Sessions: 1,
			Profile: p.Name, Inventory: map[string]string{"Узел 1С по заданию": *expected, "Экземпляр в профиле": p.ExpectedServer, "База в профиле": p.Database},
			Sections: []lab.Section{s}, Error: s.Error}
		path := filepath.Join(dir, "report.json")
		if err := errors.Join(lab.SaveJSON(path, report), lab.WriteHTML(filepath.Join(dir, "report.html"), report)); err != nil {
			return "", err
		}
		if s.Error != "" {
			return path, errors.New(s.Error)
		}
		return path, nil
	})
}
