package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func restructureCLI(args []string) error {
	f := flag.NewFlagSet("restructure-trial", flag.ContinueOnError)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	root := f.String("home", filepath.Dir(exe), "Каталог явного профиля local/stand.json")
	plan := f.String("plan", "", "Путь к JSON-плану двух копий и поставки")
	apply := f.Bool("apply", false, "Обновить обе подготовленные копии и сравнить время")
	recover := f.Bool("recover", false, "Вернуть MAXDOP второй копии по журналу предыдущего запуска")
	if err = f.Parse(args); err != nil {
		return err
	}
	if *plan == "" || f.NArg() != 0 || (*apply && *recover) {
		return errors.New("задай -plan FILE, по умолчанию выполняется только проверка; -apply и -recover несовместимы")
	}
	p, err := lab.Load(profilePath(*root))
	if err != nil {
		return err
	}
	file, err := os.Open(*plan)
	if err != nil {
		return err
	}
	defer file.Close()
	var opts lab.RestructurePlan
	dec := json.NewDecoder(file)
	dec.DisallowUnknownFields()
	if err = dec.Decode(&opts); err != nil {
		return err
	}
	if err = dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("план должен содержать один JSON-объект")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *recover {
		return lab.RecoverRestructure(ctx, p, *root, opts)
	}
	path, err := lab.RunRestructure(ctx, p, *root, opts, *apply)
	if path != "" {
		fmt.Println(path)
	}
	return err
}
