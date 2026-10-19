package composio

import (
	"context"
	"errors"
	"time"
)

// SendSlack uses a session restricted to Slack's one message tool and checks
// local tenant ownership and remote account ownership before delivery.
func (s *Service) SendSlack(ctx context.Context, tenant, id, target, text string) (err error) {
	c, err := s.active(ctx, tenant, id)
	if err != nil {
		return err
	}
	if c.Toolkit != "slack" {
		return errors.New("COMPOSIO_ACCOUNT_MISMATCH: Slack connection required")
	}
	start := time.Now()
	defer func() {
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_, _ = s.pool.Exec(recordCtx, `INSERT INTO app_call_timings(tenant_id,app,duration_ms,error,timeout)VALUES($1,'slack',$2,$3,$4)`, tenant, time.Since(start).Milliseconds(), err != nil, ctx.Err() == context.DeadlineExceeded || time.Since(start) >= s.client.options.Timeout)
	}()
	account, err := s.client.StatusToolkit(ctx, c.account, c.user, "slack")
	if err != nil {
		return err
	}
	if account.Status != "ACTIVE" {
		return errors.New("COMPOSIO_NOT_CONNECTED: Slack connection is inactive")
	}
	_, err = s.client.executeTool(ctx, c.session, c.account, "SLACK_SEND_MESSAGE", map[string]any{"channel": target, "text": text})
	return err
}
