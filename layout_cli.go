package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func fileLayoutCLI(args []string) error {
	flags := flag.NewFlagSet("file-layout", flag.ContinueOnError)
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	root := flags.String("home", filepath.Dir(exe), "Каталог измерительного профиля и протоколов")
	config := flags.String("config", "", "Путь к JSON с согласованными размерами и шагами")
	apply := flags.Bool("apply", false, "Применить план после сверки цели")
	isolated := flags.Bool("isolated-instance", false, "Экземпляр выделен для стенда; разрешить изменение tempdb")
	if e = flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 || *config == "" {
		return errors.New("задай -config с путём к утверждённому JSON")
	}
	plan, e := lab.LoadFileLayout(*config)
	if e != nil {
		return e
	}
	p, e := lab.Load(profilePath(*root))
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return deliverCommand(*root, "file-layout", func(progress func(string)) (string, error) {
		rep, path, err := lab.FileLayoutRun(ctx, p, plan, *apply, *isolated, *root)
		for _, s := range rep.Sections {
			progress(s.Title)
			progress(strings.Join(s.Columns, " | "))
			for _, r := range s.Rows {
				progress(strings.Join(r, " | "))
			}
		}
		return path, err
	})
}
