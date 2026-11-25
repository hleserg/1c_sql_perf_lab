package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func databaseStatus(ctx context.Context, root, login string) (lab.Report, error) {
	p, err := lab.Load(profilePath(root))
	if err != nil {
		return lab.Report{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return lab.AccessStatus(bounded, p, login)
}

func statusCLI(ctx context.Context, root, login string) error {
	r, err := databaseStatus(ctx, root, login)
	for _, s := range r.Sections {
		fmt.Println(s.Title)
		t := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(t, strings.Join(s.Columns, "\t"))
		for _, row := range s.Rows {
			fmt.Fprintln(t, strings.Join(row, "\t"))
		}
		t.Flush()
		if s.Error != "" {
			fmt.Println(s.Error)
		}
		fmt.Println()
	}
	return err
}

func repairAccess(ctx context.Context, root, login, role string) error {
	if strings.TrimSpace(login) == "" {
		return fmt.Errorf("укажи точный логин подключения 1С в -sql-login")
	}
	p, err := lab.Load(profilePath(root))
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if role == "db_owner" {
		fmt.Printf("Роль db_owner даёт логину %s полный контроль внутри базы %s на %s.\n", login, p.Database, p.ExpectedServer)
	}
	result, err := lab.RepairAccess(bounded, p, login, role)
	if err == nil {
		fmt.Println(result)
	}
	return err
}
