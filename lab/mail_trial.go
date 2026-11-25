package lab

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/gofrs/flock"
)

type MailOptions struct {
	Profile, To, Trial, Ack string
	Send                    bool
	ReceivedAt              time.Time
	Wait                    time.Duration
}

var mailTrialID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (o MailOptions) Validate() error {
	if o.Wait <= 0 {
		return errors.New("укажи положительное время ожидания -wait")
	}
	if o.Trial != "" {
		if !mailTrialID.MatchString(o.Trial) {
			return errors.New("неверный идентификатор проверки почты")
		}
		if o.Profile != "" || o.To != "" || o.Send {
			return errors.New("для существующей проверки укажи -trial без параметров новой отправки")
		}
		if (o.Ack == "") != o.ReceivedAt.IsZero() {
			return errors.New("для подтверждения нужны код -ack и время получения -received-at с часовым поясом")
		}
		return nil
	}
	if o.Ack != "" || !o.ReceivedAt.IsZero() {
		return errors.New("для подтверждения нужен идентификатор -trial")
	}
	if strings.TrimSpace(o.Profile) == "" || strings.TrimSpace(o.Profile) != o.Profile || len(utf16.Encode([]rune(o.Profile))) > 128 || strings.ContainsAny(o.Profile, "\r\n\x00") {
		return errors.New("укажи имя настроенного профиля Database Mail в -profile")
	}
	a, err := mail.ParseAddress(o.To)
	if err != nil || a.Address != o.To || strings.ContainsAny(o.To, ";\r\n,\x00") {
		return errors.New("укажи один почтовый адрес в -to")
	}
	return nil
}

type MailObservation struct {
	UTC    time.Time
	Status string
}

type MailTrial struct {
	ID, TargetKey, CodeSHA256    string
	RequestedUTC                 time.Time
	MailItemID                   int64  `json:",omitempty"`
	QueueTimeSQL, SentTimeSQL    string `json:",omitempty"`
	QueueDurationMS              *int64 `json:",omitempty"`
	Observations                 []MailObservation
	LastError                    string     `json:",omitempty"`
	ReceivedUTC, AcknowledgedUTC *time.Time `json:",omitempty"`
	FromRequestToReceiptSeconds  *int64     `json:",omitempty"`
}

func mailNonce(n int) (string, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}

func mailSubject(id string) string { return "SQL Perf Lab · проверка доставки " + id }

func mailCodeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// The subject is unique even if a connection is lost after committing the mail
// item but before its ID reaches the client. Resuming only reads the outbox.
func (l *Live) submitMail(ctx context.Context, t *MailTrial, o MailOptions, code string) error {
	body := "Контрольное сообщение SQL Perf Lab. Передайте оператору время получения с часовым поясом и код " + code + "."
	return l.conn.QueryRowContext(ctx, `DECLARE @id int;
EXEC msdb.dbo.sp_send_dbmail @profile_name=@p1,@recipients=@p2,@subject=@p3,@body=@p4,@mailitem_id=@id OUTPUT;
SELECT @id`, o.Profile, o.To, mailSubject(t.ID), body).Scan(&t.MailItemID)
}

func (l *Live) readMailState(ctx context.Context, t *MailTrial) (string, error) {
	var state, queued string
	var sent sql.NullString
	var delay sql.NullInt64
	var id int64
	err := l.conn.QueryRowContext(ctx, `SELECT mailitem_id,sent_status,CONVERT(varchar(23),send_request_date,121),
CASE WHEN sent_status='sent' THEN CONVERT(varchar(23),sent_date,121) END,
CASE WHEN sent_status='sent' THEN DATEDIFF_BIG(millisecond,send_request_date,sent_date) END
FROM msdb.dbo.sysmail_allitems WHERE subject=@p1 AND (mailitem_id=@p2 OR @p2=0)`, mailSubject(t.ID), t.MailItemID).Scan(&id, &state, &queued, &sent, &delay)
	if err != nil {
		return "", err
	}
	t.MailItemID = id
	t.QueueTimeSQL = queued
	t.SentTimeSQL = sent.String
	if delay.Valid && delay.Int64 >= 0 {
		ms := delay.Int64
		t.QueueDurationMS = &ms
	}
	return state, nil
}

type mailTransport interface {
	submitMail(context.Context, *MailTrial, MailOptions, string) error
	readMailState(context.Context, *MailTrial) (string, error)
}

func observeMail(t *MailTrial, state string) {
	if len(t.Observations) == 0 || t.Observations[len(t.Observations)-1].Status != state {
		t.Observations = append(t.Observations, MailObservation{UTC: time.Now().UTC(), Status: state})
	}
}

func pollMail(ctx context.Context, t *MailTrial, transport mailTransport, path string, every time.Duration) error {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		state, err := transport.readMailState(ctx, t)
		if errors.Is(err, sql.ErrNoRows) {
			state = "письмо с этим ID не видно в msdb"
		} else if err != nil {
			return err
		}
		observeMail(t, state)
		t.LastError = ""
		if err = SaveJSON(path, t); err != nil {
			return err
		}
		switch state {
		case "sent":
			return nil
		case "failed":
			return errors.New("Database Mail зарегистрировала ошибку отправки")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func confirmMail(t *MailTrial, code string, received, now time.Time) error {
	if mailCodeHash(strings.ToLower(strings.TrimSpace(code))) != t.CodeSHA256 {
		return errors.New("код не совпадает с этой проверкой")
	}
	received = received.UTC()
	now = now.UTC()
	// Mail headers and manually supplied receipt times normally have only whole seconds.
	start := t.RequestedUTC.Truncate(time.Second)
	if received.Before(start) || received.After(now) {
		return errors.New("время получения должно быть между запросом на отправку и текущим временем")
	}
	if t.ReceivedUTC != nil {
		if t.ReceivedUTC.Equal(received) {
			return nil
		}
		return errors.New("в протокол уже внесено другое время получения")
	}
	t.ReceivedUTC = &received
	t.AcknowledgedUTC = &now
	seconds := int64(received.Truncate(time.Second).Sub(start) / time.Second)
	t.FromRequestToReceiptSeconds = &seconds
	return nil
}

// MailDeliveryTest persists a local protocol before the single send attempt.
// The standalone caller must explicitly specify Send. A trial ID only permits
// polling or recording a recipient's code and timestamp.
func MailDeliveryTest(ctx context.Context, root string, p Profile, o MailOptions, notify func(string)) (t MailTrial, path string, err error) {
	if err = o.Validate(); err != nil {
		return
	}
	if o.Trial == "" && !o.Send {
		err = errors.New("для отправки нужен -send")
		return
	}
	id := o.Trial
	if id == "" {
		if id, err = mailNonce(16); err != nil {
			return
		}
	}
	dir := filepath.Join(root, "local", "mail-tests", p.Key())
	if err = os.MkdirAll(dir, 0700); err != nil {
		return
	}
	path = filepath.Join(dir, id+".json")
	lock := flock.New(path + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return
	}
	if !ok {
		err = errors.New("этот протокол почты уже открыт другой командой")
		return
	}
	defer lock.Unlock()
	var code string
	if o.Trial != "" {
		var b []byte
		b, err = os.ReadFile(path)
		if err != nil {
			return
		}
		if err = json.Unmarshal(b, &t); err != nil {
			return
		}
		if t.ID != id || t.TargetKey != p.Key() {
			err = errors.New("протокол относится к другой проверке или другому стенду")
			return
		}
		if o.Ack != "" {
			if err = confirmMail(&t, o.Ack, o.ReceivedAt, time.Now()); err == nil {
				err = SaveJSON(path, t)
			}
			return
		}
	} else {
		if _, statErr := os.Stat(path); statErr == nil {
			err = errors.New("такой протокол уже существует")
			return
		} else if !errors.Is(statErr, os.ErrNotExist) {
			err = statErr
			return
		}
		if code, err = mailNonce(8); err != nil {
			return
		}
		t = MailTrial{ID: id, TargetKey: p.Key(), CodeSHA256: mailCodeHash(code)}
	}
	l := &Live{P: p}
	defer l.Close()
	verify, cancel := context.WithTimeout(ctx, 15*time.Second)
	if err = l.connect(verify, p.Database); err == nil {
		err = l.checkMailTarget(verify)
	}
	cancel()
	if err != nil {
		return
	}
	bounded, stop := context.WithTimeout(ctx, o.Wait)
	defer stop()
	err = trackMail(bounded, &t, o, code, l, path, notify, 2*time.Second)
	return
}

func trackMail(ctx context.Context, t *MailTrial, o MailOptions, code string, transport mailTransport, path string, notify func(string), every time.Duration) error {
	var err error
	if o.Send {
		t.RequestedUTC = time.Now().UTC()
		observeMail(t, "запрос отправки")
		if err = SaveJSON(path, t); err != nil {
			return err
		}
		if notify != nil {
			notify("Проверка " + t.ID + " · " + path)
		}
		if err = transport.submitMail(ctx, t, o, code); err == nil {
			observeMail(t, "принято в очередь Database Mail")
			err = SaveJSON(path, t)
		}
	}
	if err == nil {
		err = pollMail(ctx, t, transport, path, every)
	}
	if err != nil {
		t.LastError = err.Error()
		err = errors.Join(err, SaveJSON(path, t))
	}
	return err
}

func (l *Live) checkMailTarget(ctx context.Context) error {
	var server, database string
	var id int
	err := l.conn.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')),DB_NAME(),DB_ID()`).Scan(&server, &database, &id)
	if err != nil {
		return err
	}
	if id <= 4 || !strings.EqualFold(server, l.P.ExpectedServer) || !strings.EqualFold(database, l.P.Database) {
		return fmt.Errorf("цель отличается от профиля: SQL %q, база %q", server, database)
	}
	return nil
}
