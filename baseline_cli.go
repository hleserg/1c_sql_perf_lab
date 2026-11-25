package main

import (
	"context"
	"fmt"
	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func approveBaseline(ctx context.Context, root string) error {
	p, err := lab.Load(profilePath(root))
	if err != nil {
		return err
	}
	report, _, err := runLab(root, false, nil, "inspect", nil, false, nil, ctx)
	if err != nil {
		return err
	}
	path, err := lab.ApproveReference(root, p, report)
	if err == nil {
		fmt.Println("Эталон настроек сохранён", path)
	}
	return err
}
