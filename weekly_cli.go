package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/hleserg/1c_sql_perf_lab/lab"
)

// This subcommand also serves the optional Windows Scheduled Task.
func weeklyCLI(args []string) error {
	flags := flag.NewFlagSet("weekly-verify", flag.ContinueOnError)
	root := flags.String("home", "", "Каталог измерительного профиля")
	o := lab.WeeklyOptions{}
	flags.StringVar(&o.Server, "server", "", "Ожидаемый экземпляр стенда")
	flags.StringVar(&o.Measurement, "measurement", "", "Имя базы измерительного профиля")
	flags.StringVar(&o.Source, "source", "", "Имя источника в заголовке бэкапа")
	flags.StringVar(&o.Folder, "folder", "", "Отдельная папка полных копий на сервере SQL")
	flags.DurationVar(&o.MaxAge, "max-age", 7*24*time.Hour, "Максимальный возраст бэкапа, до 168h")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *root == "" {
		return errors.New("задай -home и именованные параметры weekly-verify")
	}
	path, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	p, err := lab.Load(profilePath(path))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return deliverCommand(path, "weekly-verify", func(progress func(string)) (string, error) {
		return lab.WeeklyVerify(ctx, p, path, o, progress)
	})
}
