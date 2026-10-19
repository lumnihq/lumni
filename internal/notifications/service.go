// Package notifications delivers explicitly configured routes through durable
// jobs. Configuration never sends a message by itself.
package notifications

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"lumni/internal/secrets"
)

type Channel struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	Connection string `json:"connection_id,omitempty"`
}
type Route struct {
	Event    string   `json:"event"`
	Channels []string `json:"channels"`
	Schedule string   `json:"schedule,omitempty"`
}
type Config struct {
	Channels []Channel `json:"channels"`
	Routes   []Route   `json:"routes"`
}

func ValidEmail(s string) bool {
	a, e := mail.ParseAddress(s)
	return e == nil && a.Address == s && !strings.ContainsAny(s, "\r\n")
}
func ParseSchedule(s string) (time.Weekday, int, int, *time.Location, error) {
	parts := strings.Fields(s)
	if len(parts) != 3 {
		return 0, 0, 0, nil, errors.New("schedule must be DAY HH:MM IANA-zone")
	}
	week := map[string]time.Weekday{"SUN": time.Sunday, "MON": time.Monday, "TUE": time.Tuesday, "WED": time.Wednesday, "THU": time.Thursday, "FRI": time.Friday, "SAT": time.Saturday}
	day, ok := week[parts[0]]
	hm := strings.Split(parts[1], ":")
	if !ok || len(hm) != 2 {
		return 0, 0, 0, nil, errors.New("invalid schedule")
	}
	h, e := strconv.Atoi(hm[0])
	if e != nil || h < 0 || h > 23 {
		return 0, 0, 0, nil, errors.New("invalid schedule hour")
	}
	m, e := strconv.Atoi(hm[1])
	if e != nil || m < 0 || m > 59 {
		return 0, 0, 0, nil, errors.New("invalid schedule minute")
	}
	z, e := time.LoadLocation(parts[2])
	return day, h, m, z, e
}
func Validate(c Config) error {
	if len(c.Channels) > 100 || len(c.Routes) > 100 {
		return errors.New("at most 100 channels and routes")
	}
	ids := map[string]bool{}
	for _, ch := range c.Channels {
		if ch.ID == "" || ids[ch.ID] {
			return errors.New("unique nonempty channel ids are required")
		}
		ids[ch.ID] = true
		switch ch.Kind {
		case "email":
			if !ValidEmail(ch.Target) {
				return errors.New("invalid email target")
			}
		case "slack":
			if ch.Target == "" || ch.Connection == "" {
				return errors.New("Slack target and connection_id required")
			}
		default:
			return errors.New("channel kind must be slack or email")
		}
	}
	for _, r := range c.Routes {
		if r.Event != "approval.requested" && r.Event != "incident.opened" && r.Event != "digest.weekly" {
			return errors.New("unsupported notification event")
		}
		if len(r.Channels) == 0 {
			return errors.New("route needs channels")
		}
		for _, id := range r.Channels {
			if !ids[id] {
				return errors.New("route refers to unknown channel")
			}
		}
		if r.Event == "digest.weekly" {
			if _, _, _, _, e := ParseSchedule(r.Schedule); e != nil {
				return e
			}
		} else if r.Schedule != "" {
			return errors.New("only weekly digests accept schedule")
		}
	}
	return nil
}

type SlackSender interface {
	SendSlack(context.Context, string, string, string, string) error
}
type Service struct {
	pool   *pgxpool.Pool
	slack  SlackSender
	cipher *secrets.Manager
}

func New(pool *pgxpool.Pool, slack SlackSender) *Service       { return &Service{pool: pool, slack: slack} }
func (s *Service) WithCipher(cipher *secrets.Manager) *Service { s.cipher = cipher; return s }
func (s *Service) Once(ctx context.Context) error {
	if err := s.schedule(ctx, time.Now()); err != nil {
		return err
	}
	return s.deliver(ctx)
}
func (s *Service) schedule(ctx context.Context, now time.Time) error {
	rows, err := s.pool.Query(ctx, `SELECT tenant_id::text,channels,routes FROM notification_config WHERE routes<>'[]'`)
	if err != nil {
		return err
	}
	type conf struct {
		Tenant string
		Config
	}
	cs := []conf{}
	for rows.Next() {
		var c conf
		var ch, rs []byte
		if err = rows.Scan(&c.Tenant, &ch, &rs); err != nil {
			break
		}
		_ = json.Unmarshal(ch, &c.Channels)
		_ = json.Unmarshal(rs, &c.Routes)
		cs = append(cs, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range cs {
		for i, r := range c.Routes {
			if r.Event != "digest.weekly" {
				continue
			}
			day, h, m, z, e := ParseSchedule(r.Schedule)
			if e != nil {
				continue
			}
			local := now.In(z)
			if local.Weekday() != day || local.Hour()*60+local.Minute() < h*60+m {
				continue
			}
			date := local.Format("2006-01-02")
			tx, e := s.pool.Begin(ctx)
			if e != nil {
				return e
			}
			key := fmt.Sprintf("%d:%s", i, r.Schedule)
			tag, e := tx.Exec(ctx, `INSERT INTO notification_schedules(tenant_id,route_key,last_sent_date)VALUES($1,$2,$3)ON CONFLICT(tenant_id,route_key)DO UPDATE SET last_sent_date=EXCLUDED.last_sent_date WHERE notification_schedules.last_sent_date<>EXCLUDED.last_sent_date`, c.Tenant, key, date)
			if e == nil && tag.RowsAffected() > 0 {
				for _, id := range r.Channels {
					_, e = tx.Exec(ctx, `INSERT INTO notification_jobs(tenant_id,id,channel_id,kind,ref_id)VALUES($1,$2,$3,'digest',$4)ON CONFLICT DO NOTHING`, c.Tenant, "digest:"+key+":"+date+":"+id, id, date)
					if e != nil {
						break
					}
				}
			}
			if e == nil {
				e = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
			if e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Service) deliver(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var tenant, id, channel, kind, ref string
	var payload, channels []byte
	err = tx.QueryRow(ctx, `SELECT j.tenant_id::text,j.id,j.channel_id,j.kind,j.ref_id,j.payload,c.channels FROM notification_jobs j JOIN notification_config c ON c.tenant_id=j.tenant_id WHERE j.status='PENDING' AND j.next_attempt_at<=now() ORDER BY j.created_at FOR UPDATE OF j SKIP LOCKED LIMIT 1`).Scan(&tenant, &id, &channel, &kind, &ref, &payload, &channels)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var cs []Channel
	_ = json.Unmarshal(channels, &cs)
	var ch *Channel
	for i := range cs {
		if cs[i].ID == channel {
			ch = &cs[i]
			break
		}
	}
	var p map[string]string
	_ = json.Unmarshal(payload, &p)
	message := p["message"]
	if ch == nil {
		err = errors.New("configured channel no longer exists")
	} else {
		if message == "" {
			message, err = s.message(ctx, tenant, kind, ref)
		}
		if err == nil {
			sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			if ch.Kind == "slack" {
				if s.slack == nil {
					err = errors.New("Slack transport is not configured")
				} else {
					err = s.slack.SendSlack(sendCtx, tenant, ch.Connection, ch.Target, message)
				}
			} else {
				target := ch.Target
				if v := p["recipient"]; v != "" {
					target = v
				}
				err = SendEmail(sendCtx, target, message)
			}
			cancel()
		}
	}
	if err != nil {
		_, e := tx.Exec(ctx, `UPDATE notification_jobs SET attempts=attempts+1,last_error=$3,next_attempt_at=now()+make_interval(secs=>LEAST(3600,power(2,LEAST(attempts+1,12))::int)),status=CASE WHEN attempts>=9 THEN 'FAILED' ELSE 'PENDING' END WHERE tenant_id=$1 AND id=$2`, tenant, id, "Notification delivery failed; check channel and transport configuration")
		if e != nil {
			return e
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE notification_jobs SET status='SENT',sent_at=now(),attempts=attempts+1,last_error=NULL WHERE tenant_id=$1 AND id=$2`, tenant, id)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *Service) message(ctx context.Context, tenant, kind, ref string) (string, error) {
	switch kind {
	case "incident":
		var title string
		err := s.pool.QueryRow(ctx, `SELECT title FROM incidents WHERE tenant_id=$1 AND id=$2`, tenant, ref).Scan(&title)
		return "Lumni incident: " + title, err
	case "approval":
		var op string
		err := s.pool.QueryRow(ctx, `SELECT a.operation FROM approvals p JOIN actions a ON a.tenant_id=p.tenant_id AND a.id=p.action_id WHERE p.tenant_id=$1 AND p.id::text=$2`, tenant, ref).Scan(&op)
		return "Lumni approval requested: " + op, err
	case "digest":
		var n, kept int64
		err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(total),0),COALESCE(sum(kept_micros_usd),0) FROM console_metrics WHERE tenant_id=$1 AND bucket>=now()-interval '7 days'`, tenant).Scan(&n, &kept)
		return fmt.Sprintf("Lumni weekly digest: %d actions checked; $%.2f kept from repeats.", n, float64(kept)/1e6), err
	case "proof":
		var ciphertext []byte
		err := s.pool.QueryRow(ctx, `SELECT url_ciphertext FROM proofs WHERE tenant_id=$1 AND id=$2 AND revoked_at IS NULL AND expires_at>now()`, tenant, ref).Scan(&ciphertext)
		if err != nil {
			return "", err
		}
		if s.cipher == nil {
			return "", errors.New("proof encryption transport is not configured")
		}
		plain, err := s.cipher.Decrypt(ciphertext)
		return "Lumni proof: " + string(plain), err
	}
	return "", errors.New("unsupported notification kind")
}
func SendEmail(ctx context.Context, to, body string) error {
	addr, from := os.Getenv("LUMNI_SMTP_ADDR"), os.Getenv("LUMNI_SMTP_FROM")
	if addr == "" || !ValidEmail(from) || !ValidEmail(to) {
		return errors.New("SMTP transport or recipient is not configured")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(20 * time.Second)
	if v, ok := ctx.Deadline(); ok {
		deadline = v
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return errors.New("SMTP requires STARTTLS")
	}
	if user := os.Getenv("LUMNI_SMTP_USER"); user != "" {
		if err = client.Auth(smtp.PlainAuth("", user, os.Getenv("LUMNI_SMTP_PASSWORD"), host)); err != nil {
			return err
		}
	}
	if err = client.Mail(from); err != nil {
		return err
	}
	if err = client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: Lumni notification\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", from, to, strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	if err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return client.Quit()
}
