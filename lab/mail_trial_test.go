package lab

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The integration suite exercises only a catalog lookup for a fresh ID. It
// never calls sp_send_dbmail or installs/enables a mail profile.
func testMailSQL(t *testing.T, ctx context.Context, l *Live) {
	t.Helper()
	if err := l.checkMailTarget(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := mailNonce(16)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.readMailState(ctx, &MailTrial{ID: id}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mail catalog lookup: %v", err)
	}
}

func TestMailExplicitProfileAndOneRecipient(t *testing.T) {
	valid := MailOptions{Profile: "stand", To: "operator@example.test", Wait: time.Minute}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := MailDeliveryTest(context.Background(), t.TempDir(), Example(), valid, nil); err == nil || !strings.Contains(err.Error(), "-send") {
		t.Fatal("preview reached send path", err)
	}
	for _, to := range []string{"", "a@example.test;b@example.test", "a@example.test,b@example.test", "user@example.test\r\nBcc:x@example.test"} {
		o := valid
		o.To = to
		if o.Validate() == nil {
			t.Fatalf("accepted %q", to)
		}
	}
	for _, o := range []MailOptions{
		{Profile: "", To: "a@example.test", Send: true, Wait: time.Minute},
		{Trial: "../other", Wait: time.Minute},
		{Trial: strings.Repeat("a", 32), Ack: "token", Wait: time.Minute},
		{Trial: strings.Repeat("a", 32), Send: true, Wait: time.Minute},
	} {
		if o.Validate() == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
}

type testMailTransport struct {
	sends, reads int
	states       []string
	submitErr    error
}

func (m *testMailTransport) submitMail(context.Context, *MailTrial, MailOptions, string) error {
	m.sends++
	return m.submitErr
}
func (m *testMailTransport) readMailState(_ context.Context, t *MailTrial) (string, error) {
	t.MailItemID = 321
	v := m.states[min(m.reads, len(m.states)-1)]
	m.reads++
	return v, nil
}

func TestMailSubmissionAndResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trial.json")
	trial := MailTrial{ID: strings.Repeat("a", 32)}
	tr := &testMailTransport{states: []string{"unsent", "unsent", "retrying", "sent"}}
	err := trackMail(context.Background(), &trial, MailOptions{Send: true}, "returned-code", tr, path, nil, time.Millisecond)
	if err != nil || tr.sends != 1 || tr.reads != 4 || trial.MailItemID != 321 || trial.ReceivedUTC != nil {
		t.Fatal(err, tr, trial)
	}
	if len(trial.Observations) != 5 {
		t.Fatal("duplicate/missing status transitions", trial.Observations)
	}
	if err = trackMail(context.Background(), &trial, MailOptions{Trial: trial.ID}, "", tr, path, nil, time.Millisecond); err != nil || tr.sends != 1 {
		t.Fatal("resending on resume", err, tr)
	}
	tr.submitErr = errors.New("connection lost after enqueue")
	trial = MailTrial{ID: strings.Repeat("b", 32)}
	if err = trackMail(context.Background(), &trial, MailOptions{Send: true}, "code", tr, path, nil, time.Millisecond); err == nil || trial.LastError == "" {
		t.Fatal("missing evidence after uncertain enqueue", err)
	}
	if err = trackMail(context.Background(), &trial, MailOptions{Trial: trial.ID}, "", tr, path, nil, time.Millisecond); err != nil || tr.sends != 2 || trial.LastError != "" {
		t.Fatal("uncertain send was repeated", tr, err)
	}
}

func TestMailTimeoutFailureAndReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trial.json")
	now := time.Now().UTC()
	trial := MailTrial{CodeSHA256: mailCodeHash("abc123"), RequestedUTC: now.Add(-time.Hour)}
	tr := &testMailTransport{states: []string{"retrying"}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := trackMail(ctx, &trial, MailOptions{}, "", tr, path, nil, time.Hour); !errors.Is(err, context.DeadlineExceeded) || trial.LastError == "" || trial.ReceivedUTC != nil {
		t.Fatal(err, trial)
	}
	tr.states = []string{"failed"}
	if err := trackMail(context.Background(), &trial, MailOptions{}, "", tr, path, nil, time.Hour); err == nil || trial.LastError == "" {
		t.Fatal("failed SMTP accepted", trial)
	}
	received := now.Add(-time.Minute)
	if confirmMail(&trial, "wrong", received, now) == nil || confirmMail(&trial, "abc123", now.Add(time.Hour), now) == nil {
		t.Fatal("invalid receipt")
	}
	if err := confirmMail(&trial, "ABC123", received, now); err != nil || *trial.FromRequestToReceiptSeconds != 59*60 {
		t.Fatal(err, trial)
	}
	if err := confirmMail(&trial, "abc123", received, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := confirmMail(&trial, "abc123", received.Add(time.Second), now); err == nil {
		t.Fatal("changed prior confirmation")
	}
}

func TestMailReceiptRecordsLocallyWithOriginalTimestamp(t *testing.T) {
	root := t.TempDir()
	p := Example()
	p.SQLHost = "unreachable.invalid"
	now := time.Now().UTC().Truncate(time.Second)
	trial := MailTrial{ID: strings.Repeat("c", 32), TargetKey: p.Key(), CodeSHA256: mailCodeHash("ab01"), RequestedUTC: now.Add(-2 * time.Minute).Add(200 * time.Millisecond)}
	path := filepath.Join(root, "local", "mail-tests", p.Key(), trial.ID+".json")
	if err := SaveJSON(path, trial); err != nil {
		t.Fatal(err)
	}
	received := now.Add(-2 * time.Minute)
	opts := MailOptions{Trial: trial.ID, Ack: "ab01", ReceivedAt: received, Wait: time.Minute}
	got, file, err := MailDeliveryTest(context.Background(), root, p, opts, nil)
	if err != nil || path != file || got.ReceivedUTC == nil || !got.ReceivedUTC.Equal(received) || *got.FromRequestToReceiptSeconds != 0 {
		t.Fatal(got, file, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), "ab01") {
		t.Fatal("receipt code persisted in plaintext", err)
	}
}
