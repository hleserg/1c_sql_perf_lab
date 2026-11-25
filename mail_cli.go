package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func mailCLI(args []string) error {
	exe, _ := os.Executable()
	flags := flag.NewFlagSet("mail-test", flag.ContinueOnError)
	root := flags.String("home", filepath.Dir(exe), "Каталог профиля стенда")
	var o lab.MailOptions
	flags.StringVar(&o.Profile, "profile", "", "Настроенный профиль SQL Database Mail")
	flags.StringVar(&o.To, "to", "", "Один адрес получателя")
	flags.BoolVar(&o.Send, "send", false, "Поставить одно тестовое письмо в очередь Database Mail")
	flags.DurationVar(&o.Wait, "wait", 2*time.Minute, "Предельное ожидание статуса Database Mail")
	flags.StringVar(&o.Trial, "trial", "", "Идентификатор существующего протокола")
	flags.StringVar(&o.Ack, "ack", "", "Код из полученного письма")
	received := flags.String("received-at", "", "Время получения с часовым поясом в формате RFC3339")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("неизвестные аргументы mail-test")
	}
	if *received != "" {
		var err error
		o.ReceivedAt, err = time.Parse(time.RFC3339, *received)
		if err != nil {
			return fmt.Errorf("время -received-at: %w", err)
		}
	}
	if err := o.Validate(); err != nil {
		return err
	}
	if o.Trial == "" && !o.Send {
		fmt.Printf("Предпросмотр письма для %s через профиль %q. Для постановки в очередь добавь -send.\n", o.To, o.Profile)
		return nil
	}
	path, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	p, err := lab.Load(profilePath(path))
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	t, file, err := lab.MailDeliveryTest(ctx, path, p, o, func(msg string) { fmt.Println(msg) })
	if _, statErr := os.Stat(file); t.ID != "" && statErr == nil {
		fmt.Println("Проверка", t.ID)
		fmt.Println(file)
		for _, s := range t.Observations {
			fmt.Printf("%s · %s\n", s.UTC.Format(time.RFC3339), s.Status)
		}
		if t.QueueDurationMS != nil {
			fmt.Printf("От очереди до sent по часам SQL: %d мс.\n", *t.QueueDurationMS)
		}
		if t.ReceivedUTC != nil && t.FromRequestToReceiptSeconds != nil {
			fmt.Printf("Получатель подтвердил %s · от запроса на отправку около %d с.\n", t.ReceivedUTC.Format(time.RFC3339), *t.FromRequestToReceiptSeconds)
		} else {
			fmt.Println("Статус sent означает передачу SMTP-серверу. Время получения подтвердит адресат.")
			fmt.Println("Сверить статус: mail-test -trial", t.ID)
			fmt.Println("Записать подтверждение: mail-test -trial", t.ID, "-ack <код> -received-at <время RFC3339>")
		}
	}
	return err
}
