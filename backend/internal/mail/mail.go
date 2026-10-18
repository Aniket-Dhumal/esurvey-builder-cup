// Package mail queues and sends the platform's outbound email.
//
// # Why a queue and not a function that sends
//
// The same reason internal/notify queues its SMS: no mail server is
// configured on most installs yet, and a mailer that quietly dropped messages
// when unconfigured would report every invitation as sent while none of them
// arrived. Queueing makes the backlog the honest measure of what is not being
// delivered, and everything queued is still there when credentials are set.
//
// It also makes account creation atomic in the way that matters. Creating an
// officer writes a user row, a role grant, an invitation and a queued email;
// if delivery were a network call inside that path, an SMTP timeout would
// either roll back an account that was correctly created or leave one behind
// with no way to claim it.
//
// # Plain SMTP on purpose
//
// Not an API client for one vendor. Every place this is likely to run -- a
// NIC relay, Google Workspace, SES, a departmental Exchange -- speaks SMTP,
// and the deployment picks one with five environment variables rather than a
// code change.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Purposes. Stored, so they outlive any enum here.
const (
	PurposeInvite        = "account.invite"
	PurposePasswordReset = "account.reset"
)

// ErrNotConfigured is returned by a sender with no SMTP host. Callers treat it
// as "leave it queued", never as a delivery failure to retry-count.
var ErrNotConfigured = errors.New("no SMTP server is configured")

// Message is one email waiting to go out.
type Message struct {
	To      string
	Subject string
	Body    string

	Purpose      string
	ResourceType string
	ResourceID   string
}

// Config is the SMTP server and the identity messages are sent as.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string

	// From is the envelope and header sender, e.g.
	// "eSurvey <no-reply@esurvey.example>".
	From string

	// StartTLS upgrades a plaintext connection on port 587. When false and
	// the port is 465, the connection is TLS from the first byte. Either way
	// the credential is never sent in the clear -- see dial.
	StartTLS bool

	// Timeout bounds one delivery attempt, connection included.
	Timeout time.Duration
}

// Configured reports whether a server has been named.
func (c Config) Configured() bool { return strings.TrimSpace(c.Host) != "" }

// Mailer queues messages and, when configured, delivers them.
type Mailer struct {
	pool *db.Pool
	cfg  Config
	log  *slog.Logger
}

func New(pool *db.Pool, cfg Config, log *slog.Logger) *Mailer {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	return &Mailer{pool: pool, cfg: cfg, log: log}
}

// Configured reports whether delivery is possible at all. The console asks so
// that it can show an administrator the invitation link to pass on by hand
// rather than telling them an email is on its way that never will be.
func (m *Mailer) Configured() bool { return m != nil && m.cfg.Configured() }

// Queue records a message for delivery. It never contacts the mail server, so
// it is safe inside the transaction that created the account it is about.
func (m *Mailer) Queue(ctx context.Context, q db.Querier, msg Message) error {
	if strings.TrimSpace(msg.To) == "" {
		return errors.New("mail: no recipient")
	}
	var resourceID any
	if msg.ResourceID != "" {
		resourceID = msg.ResourceID
	}
	_, err := q.Exec(ctx, `
		INSERT INTO outbound_email (to_email, subject, body, purpose,
		                            resource_type, resource_id)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6)`,
		msg.To, msg.Subject, msg.Body, msg.Purpose, msg.ResourceType, resourceID)
	return err
}

// Pending is how many messages are waiting. The number an operator needs to
// see on a status page: it is the count of people this platform has failed to
// tell something.
func (m *Mailer) Pending(ctx context.Context) (int, error) {
	var n int
	err := m.pool.QueryRow(ctx,
		`SELECT count(*) FROM outbound_email WHERE sent_at IS NULL`).Scan(&n)
	return n, err
}

// Drain sends up to limit queued messages, oldest first.
//
// Each message is claimed with FOR UPDATE SKIP LOCKED so that two API
// instances draining the same queue send each message once rather than twice
// -- a duplicated invitation is a second live credential for one account.
func (m *Mailer) Drain(ctx context.Context, limit int) (sent int, err error) {
	if !m.Configured() {
		return 0, ErrNotConfigured
	}
	for i := 0; i < limit; i++ {
		ok, err := m.one(ctx)
		if err != nil {
			return sent, err
		}
		if !ok {
			return sent, nil // queue empty
		}
		sent++
	}
	return sent, nil
}

func (m *Mailer) one(ctx context.Context) (bool, error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var id int64
	var to, subject, body string
	err = tx.QueryRow(ctx, `
		SELECT id, to_email, subject, body
		  FROM outbound_email
		 WHERE sent_at IS NULL
		 ORDER BY created_at
		 LIMIT 1
		 FOR UPDATE SKIP LOCKED`).Scan(&id, &to, &subject, &body)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // queue empty
		}
		return false, err
	}

	sendErr := m.send(ctx, to, subject, body)
	if sendErr != nil {
		// The attempt is recorded on the row rather than only in a log, so an
		// administrator can see why an invitation has not arrived without
		// being handed the API's logs.
		//
		// Deliberately not committed as "sent": the message stays queued and
		// is tried again. A mail server that is down for an hour should cost
		// an hour's delay, not a lost account.
		if _, e := tx.Exec(ctx, `
			UPDATE outbound_email
			   SET attempts = attempts + 1, last_error = $2
			 WHERE id = $1`, id, truncate(sendErr.Error(), 500)); e != nil {
			return false, e
		}
		if e := tx.Commit(ctx); e != nil {
			return false, e
		}
		return false, fmt.Errorf("sending message %d: %w", id, sendErr)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE outbound_email SET sent_at = now(), attempts = attempts + 1,
		        last_error = NULL WHERE id = $1`, id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// send delivers one message over SMTP.
func (m *Mailer) send(ctx context.Context, to, subject, body string) error {
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()

	c, err := m.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	if m.cfg.Username != "" {
		// PlainAuth refuses to send a password over an unencrypted link, so
		// this is also the check that a misconfigured StartTLS does not leak
		// the mail credential.
		auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("authenticating: %w", err)
		}
	}

	from, err := mail.ParseAddress(m.cfg.From)
	if err != nil {
		return fmt.Errorf("ESURVEY_SMTP_FROM is not an address: %w", err)
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write([]byte(compose(m.cfg.From, to, subject, body))); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func (m *Mailer) dial(ctx context.Context) (*smtp.Client, error) {
	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprint(m.cfg.Port))
	d := &net.Dialer{}
	tlsCfg := &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}

	// Implicit TLS. The whole session is encrypted, credential included.
	if m.cfg.Port == 465 && !m.cfg.StartTLS {
		conn, err := tls.DialWithDialer(d, "tcp", addr, tlsCfg)
		if err != nil {
			return nil, fmt.Errorf("connecting to %s: %w", addr, err)
		}
		return smtp.NewClient(conn, m.cfg.Host)
	}

	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}
	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return nil, err
	}
	if m.cfg.StartTLS {
		if err := c.StartTLS(tlsCfg); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("STARTTLS: %w", err)
		}
	}
	return c, nil
}

// compose builds the RFC 5322 message.
//
// The subject is Q-encoded because these carry Marathi, and a raw UTF-8
// Subject header is not something every relay between here and a district
// office will pass through intact.
func compose(from, to, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	// These are operational messages to a named officer, not a mailing list,
	// but an auto-responder loop between two of these accounts would still be
	// a mail loop.
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("\r\n")
	b.WriteString(normaliseCRLF(body))
	return b.String()
}

// normaliseCRLF converts bare newlines, and escapes a line that is a single
// dot -- which would otherwise end the DATA command early and truncate the
// message.
func normaliseCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l == "." {
			lines[i] = ".."
		}
	}
	return strings.Join(lines, "\r\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Run drains the queue on a ticker until the context is cancelled.
func (m *Mailer) Run(ctx context.Context, every time.Duration) {
	if !m.Configured() {
		m.log.Info("no SMTP server configured; invitations will queue",
			"hint", "set ESURVEY_SMTP_HOST to deliver them")
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := m.Drain(ctx, 50)
			if err != nil && !errors.Is(err, ErrNotConfigured) {
				m.log.Warn("draining the mail queue", "sent", n, "error", err)
			}
		}
	}
}
