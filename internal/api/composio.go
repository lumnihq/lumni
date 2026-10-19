package api

import (
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/lumnihq/lumni/internal/composio"
)

func (s *Server) composioRoutes(r chi.Router) {
	r.Get("/integrations/composio/connections", s.withScope("query", s.composioConnections))
	r.Post("/integrations/composio/connections", s.withScope("admin", s.composioConnect))
	r.Get("/integrations/composio/connections/{id}", s.withScope("admin", s.composioStatus))
	r.Delete("/integrations/composio/connections/{id}", s.withScope("admin", s.composioDisconnect))
	r.Get("/integrations/composio/connections/{id}/pull-requests/{number}", s.withScope("query", s.composioRead))
	r.Post("/integrations/composio/connections/{id}/execute", s.withScope("admission", s.composioExecute))
	r.Get("/integrations/composio/connections/{id}/metrics", s.withScope("query", s.composioMetrics))
}
func (s *Server) composioEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.composio == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "COMPOSIO_DISABLED", "Composio integration is not configured", nil)
		return false
	}
	return true
}
func (s *Server) composioRespond(w http.ResponseWriter, r *http.Request, status int, out any, err error) {
	if composio.IsNotFound(err) {
		s.fail(w, r, http.StatusNotFound, "NOT_FOUND", "Connection or resource was not found", nil)
		return
	}
	s.respond(w, r, status, out, err)
}
func (s *Server) composioConnections(w http.ResponseWriter, r *http.Request) {
	if s.composio == nil {
		writeJSON(w, 200, map[string]any{"enabled": false, "items": []composio.Connection{}})
		return
	}
	out, err := s.composio.List(r.Context(), tenant(r))
	s.composioRespond(w, r, 200, map[string]any{"enabled": true, "items": out}, err)
}
func (s *Server) composioConnect(w http.ResponseWriter, r *http.Request) {
	if !s.composioEnabled(w, r) {
		return
	}
	var in composio.ConnectInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.composio.Connect(r.Context(), tenant(r), in)
	s.composioRespond(w, r, 201, out, err)
}
func (s *Server) composioStatus(w http.ResponseWriter, r *http.Request) {
	if !s.composioEnabled(w, r) {
		return
	}
	out, err := s.composio.Status(r.Context(), tenant(r), chi.URLParam(r, "id"))
	s.composioRespond(w, r, 200, out, err)
}
func (s *Server) composioDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.composioEnabled(w, r) {
		return
	}
	err := s.composio.Disconnect(r.Context(), tenant(r), chi.URLParam(r, "id"))
	s.composioRespond(w, r, 200, map[string]bool{"disconnected": err == nil}, err)
}
func (s *Server) composioRead(w http.ResponseWriter, r *http.Request) {
	if !s.composioEnabled(w, r) {
		return
	}
	number, err := strconv.Atoi(chi.URLParam(r, "number"))
	if err != nil || number <= 0 {
		s.fail(w, r, 400, "INVALID_NUMBER", "Positive pull request number required", nil)
		return
	}
	out, err := s.composio.Read(r.Context(), tenant(r), chi.URLParam(r, "id"), number)
	s.composioRespond(w, r, 200, out, err)
}
func (s *Server) composioExecute(w http.ResponseWriter, r *http.Request) {
	if !s.composioEnabled(w, r) {
		return
	}
	var in composio.ExecuteInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.composio.Execute(r.Context(), tenant(r), chi.URLParam(r, "id"), in)
	s.composioRespond(w, r, 200, out, err)
}
func (s *Server) composioMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.composioEnabled(w, r) {
		return
	}
	out, err := s.composio.Metrics(r.Context(), tenant(r), chi.URLParam(r, "id"))
	s.composioRespond(w, r, 200, map[string]any{"items": out}, err)
}
func (s *Server) composioWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.composioEnabled(w, r) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.bodyLimit))
	defer r.Body.Close()
	if err != nil {
		s.fail(w, r, 400, "INVALID_BODY", "Webhook body is invalid or too large", nil)
		return
	}
	if !s.composio.VerifyWebhook(r.Header, body) {
		s.fail(w, r, 401, "INVALID_SIGNATURE", "Invalid or expired webhook signature", nil)
		return
	}
	if r.Header.Get("x-composio-webhook-version") != "V3" {
		s.fail(w, r, 400, "INVALID_VERSION", "Composio V3 webhook payload required", nil)
		return
	}
	recorded, err := s.composio.Ingest(r.Context(), body, r.Header.Get("webhook-id"))
	s.composioRespond(w, r, 200, map[string]bool{"recorded": recorded}, err)
}
