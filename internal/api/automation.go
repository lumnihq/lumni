package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lumnihq/lumni/internal/automation"
	"github.com/lumnihq/lumni/internal/compiled"
	"github.com/lumnihq/lumni/internal/contracts"
	"github.com/lumnihq/lumni/pkg/protocol"
)

type automationByKey struct{}

func (s *Server) automationRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				by := s.principalName(r)
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), automationByKey{}, by)))
			})
		})
		r.Get("/contracts", s.withScope("query", s.contractList))
		r.Post("/contracts", s.withScope("admin", s.contractSave))
		r.Post("/contracts/classify", s.withScope("query", s.contractClassify))
		r.Post("/contracts/preview", s.withScope("query", s.contractPreview))
		r.Get("/contracts/{id}", s.withScope("query", s.contractGet))
		r.Patch("/contracts/{id}", s.withScope("admin", s.contractSave))
		r.Delete("/contracts/{id}", s.withScope("admin", s.contractDelete))
		r.Put("/contracts/{id}/mode", s.withScope("admin", s.contractMode))
		r.Get("/contracts/{id}/needs", s.withScope("query", s.contractNeeds))
		r.Get("/contracts/{id}/hook-status", s.withScope("query", s.contractHook))
		r.Get("/contracts/{id}/week", s.withScope("query", s.contractWeek))
		r.Get("/contracts/{id}/runs", s.withScope("query", s.contractRuns))
		r.Post("/contracts/{id}/runs/{action_id}/evidence", s.withScope("verification", s.contractWatchEvidence))
		r.Get("/contracts/{id}/tests", s.withScope("payload", s.contractTests))
		r.Get("/tool-bindings", s.withScope("query", s.bindingList))
		r.Put("/tool-bindings/{id}", s.withScope("admin", s.bindingPut))
		r.Delete("/tool-bindings/{id}", s.withScope("admin", s.bindingDelete))
		r.Get("/integrations/{app}/grants", s.withScope("admin", s.grants))
		r.Post("/integrations/{app}/grants", s.withScope("admin", s.grants))
		r.Delete("/integrations/{app}/grants/{id}", s.withScope("admin", s.grants))
		r.Get("/compiled-paths", s.withScope("query", s.pathList))
		r.Get("/compiled-paths/totals", s.withScope("query", s.pathTotals))
		r.Get("/compiled-paths/suggestions", s.withScope("query", s.pathSuggestions))
		r.Post("/compiled-paths/discover", s.withScope("admin", s.pathDiscover))
		r.Post("/compiled-paths/route", s.withScope("runtime", s.pathRoute))
		r.Get("/compiled-paths/{id}", s.withScope("query", s.pathGet))
		r.Get("/compiled-paths/{id}/fallbacks", s.withScope("query", s.pathFallbacks))
		r.Get("/compiled-paths/{id}/race", s.withScope("query", s.pathRace))
		r.Get("/compiled-paths/{id}/versions/{v}/replay", s.withScope("query", s.pathReplay))
		for _, verb := range []string{"compile", "recompile", "shadow", "promote", "rollback", "pause", "resume"} {
			r.Post("/compiled-paths/{id}/"+verb, s.withScope("admin", s.pathTransition(verb)))
		}
		r.Put("/compiled-paths/{id}/disagreements/{case_id}/label", s.withScope("admin", s.pathLabel))
		r.Post("/compiled-paths/{id}/execute", s.withScope("runtime", s.pathExecute))
		r.Post("/compiled-paths/{id}/runs", s.withScope("runtime", s.pathReport))
		r.Get("/compiled-runs/{id}", s.withScope("query", s.pathRun))
		r.Post("/traces", s.withScope("ingest", s.tracePut))
		r.Get("/traces", s.withScope("query", s.traceList))
		r.Get("/traces/{id}/payload", s.withScope("payload", s.tracePayload))
		r.Get("/jobs/{id}", s.withScope("query", s.automationJob))
		r.Get("/config/compiler", s.withScope("admin", s.compilerConfig))
		r.Put("/config/compiler", s.withScope("admin", s.compilerConfig))

	})
}
func (s *Server) contractsService() *contracts.Service { return contracts.New(s.pool, s.secrets) }
func (s *Server) pathsService() *compiled.Service      { return s.compiled }
func (s *Server) automationRespond(w http.ResponseWriter, r *http.Request, status int, out any, e error) {
	if e == nil {
		if status == 204 {
			w.WriteHeader(204)
		} else {
			s.respond(w, r, status, out, nil)
		}
		return
	}
	var f *automation.Fault
	if errors.As(e, &f) {
		s.fail(w, r, f.Status, f.Code, f.Message, nil)
		return
	}
	if errors.Is(e, pgx.ErrNoRows) {
		s.fail(w, r, 404, "NOT_FOUND", "Entity was not found", nil)
		return
	}
	s.respond(w, r, 0, nil, e)
}
func (s *Server) strict(w http.ResponseWriter, r *http.Request, out any) bool {
	reader := http.MaxBytesReader(w, r.Body, s.bodyLimit)
	defer reader.Close()
	d := json.NewDecoder(reader)
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		s.fail(w, r, 400, "MALFORMED_JSON", "Invalid request body", map[string]any{"cause": e.Error()})
		return false
	}
	var extra any
	if e := d.Decode(&extra); errors.Is(e, io.EOF) {
		return true
	}
	s.fail(w, r, 400, "MALFORMED_JSON", "Expected one JSON value", nil)
	return false
}
func (s *Server) contractGet(w http.ResponseWriter, r *http.Request) {
	v, e := s.contractsService().Get(r.Context(), tenant(r), pathID(r, "id"))
	s.automationRespond(w, r, 200, v, e)
}
func (s *Server) contractList(w http.ResponseWriter, r *http.Request) {
	s.pagedList(w, r, listSpec{From: "contracts c JOIN tenants t ON t.id=c.tenant_id", Tenant: "c.tenant_id", ID: "c.id", Key: "c.id", Time: "c.created_at", Mode: "c.mode", Search: "c.title||' '||c.text", Condition: "c.deleted_at IS NULL", JSON: `to_jsonb(c)-'tenant_id'-'deleted_at'||jsonb_build_object('effective_mode',` + contracts.EffectiveModeSQL + `)`}, "", false)
}
func (s *Server) contractSave(w http.ResponseWriter, r *http.Request) {
	var in protocol.ContractInput
	if !s.strict(w, r, &in) {
		return
	}
	id := pathID(r, "id")
	status := 201
	if id != "" {
		status = 200
		old, e := s.contractsService().Get(r.Context(), tenant(r), id)
		if e != nil {
			s.automationRespond(w, r, 0, nil, e)
			return
		}
		if in.Title == "" {
			in.Title = old.Title
		}
		if in.Text == "" {
			in.Text = old.Text
		}
		if in.Rules == nil {
			in.Rules = old.Rules
		}
		if in.Revision < 1 {
			s.automationRespond(w, r, 0, nil, automation.Invalid("revision required for edit"))
			return
		}
	}
	v, e := s.contractsService().Save(r.Context(), tenant(r), s.principalName(r), id, in)
	s.automationRespond(w, r, status, v, e)
}
func (s *Server) contractDelete(w http.ResponseWriter, r *http.Request) {
	e := s.contractsService().Delete(r.Context(), tenant(r), s.principalName(r), pathID(r, "id"))
	s.automationRespond(w, r, 204, nil, e)
}
func (s *Server) contractMode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode     string `json:"mode"`
		Revision int    `json:"revision"`
	}
	if !s.strict(w, r, &in) {
		return
	}
	v, e := s.contractsService().Mode(r.Context(), tenant(r), s.principalName(r), pathID(r, "id"), in.Mode, in.Revision)
	s.automationRespond(w, r, 200, v, e)
}
func (s *Server) contractNeeds(w http.ResponseWriter, r *http.Request) {
	c, e := s.contractsService().Get(r.Context(), tenant(r), pathID(r, "id"))
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	v, e := s.contractsService().Ready(r.Context(), tenant(r), c.Rules)
	s.automationRespond(w, r, 200, v, e)
}
func (s *Server) bindingList(w http.ResponseWriter, r *http.Request) {
	s.pagedList(w, r, listSpec{From: "tool_bindings b", Tenant: "b.tenant_id", ID: "b.id", Key: "b.id", Time: "b.created_at", JSON: "b.document||jsonb_build_object('enabled',b.enabled)"}, "", false)
}
func (s *Server) bindingPut(w http.ResponseWriter, r *http.Request) {
	var in protocol.ToolBinding
	if !s.strict(w, r, &in) {
		return
	}
	v, e := automation.New(s.pool, s.secrets).Put(r.Context(), tenant(r), s.principalName(r), pathID(r, "id"), in)
	s.automationRespond(w, r, 200, v, e)
}
func (s *Server) bindingDelete(w http.ResponseWriter, r *http.Request) {
	e := automation.Transaction(r.Context(), s.pool, func(tx pgx.Tx) error {
		tag, e := tx.Exec(r.Context(), `UPDATE tool_bindings SET enabled=false,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND enabled`, tenant(r), pathID(r, "id"))
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return automation.Missing("enabled binding not found")
		}
		return automation.Audit(r.Context(), tx, tenant(r), s.principalName(r), "binding.disabled", pathID(r, "id"), nil, nil)
	})
	s.automationRespond(w, r, 204, nil, e)
}
func (s *Server) grants(w http.ResponseWriter, r *http.Request) {
	app := pathID(r, "app")
	if r.Method == "GET" {
		s.pagedList(w, r, listSpec{From: "integration_read_grants g", Tenant: "g.tenant_id", Key: "g.id", ID: "g.app", Time: "g.created_at", JSON: "to_jsonb(g)-'tenant_id'"}, app, false)
		return
	}
	if r.Method == "DELETE" {
		e := automation.Transaction(r.Context(), s.pool, func(tx pgx.Tx) error {
			tag, e := tx.Exec(r.Context(), `UPDATE integration_read_grants SET revoked_at=now() WHERE tenant_id=$1 AND app=$2 AND id=$3 AND revoked_at IS NULL`, tenant(r), app, pathID(r, "id"))
			if e != nil {
				return e
			}
			if tag.RowsAffected() == 0 {
				return automation.Missing("active grant not found")
			}
			return automation.Audit(r.Context(), tx, tenant(r), s.principalName(r), "read-grant.revoked", pathID(r, "id"), nil, nil)
		})
		s.automationRespond(w, r, 204, nil, e)
		return
	}
	var in struct {
		BindingID string              `json:"binding_id"`
		Objects   map[string][]string `json:"objects"`
	}
	if !s.strict(w, r, &in) {
		return
	}
	b, e := automation.New(s.pool, s.secrets).Get(r.Context(), tenant(r), in.BindingID, 0)
	if e != nil {
		s.automationRespond(w, r, 0, nil, e)
		return
	}
	if !b.ReadOnly || b.App != app || len(in.Objects) == 0 {
		s.automationRespond(w, r, 0, nil, automation.Invalid("read-only app binding and objects required"))
		return
	}
	for object, fields := range in.Objects {
		allowed, ok := b.Objects[object]
		if !ok {
			s.automationRespond(w, r, 0, nil, automation.Invalid("unregistered object"))
			return
		}
		for _, field := range fields {
			if !contracts.Has(allowed, field) {
				s.automationRespond(w, r, 0, nil, automation.Invalid("unregistered read field"))
				return
			}
		}
	}
	id := "grant_" + uuid.NewString()
	e = automation.Transaction(r.Context(), s.pool, func(tx pgx.Tx) error {
		var version int
		if e := tx.QueryRow(r.Context(), `SELECT version FROM tool_bindings WHERE tenant_id=$1 AND id=$2 AND enabled FOR SHARE`, tenant(r), b.ID).Scan(&version); e != nil {
			return e
		}
		if version != b.Version {
			return automation.Conflict("binding changed")
		}
		if _, e := tx.Exec(r.Context(), `INSERT INTO integration_read_grants(tenant_id,id,app,binding_id,binding_version,objects)VALUES($1,$2,$3,$4,$5,$6)`, tenant(r), id, app, b.ID, b.Version, automation.JSON(in.Objects)); e != nil {
			return e
		}
		return automation.Audit(r.Context(), tx, tenant(r), s.principalName(r), "read-grant.registered", id, nil, in)
	})
	s.automationRespond(w, r, 201, map[string]any{"id": id, "binding_id": b.ID, "version": b.Version, "objects": in.Objects, "provenance": "operator_configured", "provider_permissions_verified": false}, e)
}
func (s *Server) compilerConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		var data []byte
		e := s.pool.QueryRow(r.Context(), `SELECT jsonb_build_object('enabled',COALESCE((settings->>'compiler_enabled')::boolean,false),'cloud_processing_consent',COALESCE((settings->>'compiler_cloud_consent')::boolean,false)) FROM tenants WHERE id=$1`, tenant(r)).Scan(&data)
		var out map[string]any
		if e == nil {
			e = json.Unmarshal(data, &out)
			out["server_configured"] = s.compiled.Generator != nil
		}
		s.automationRespond(w, r, 200, out, e)
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
		Consent bool `json:"cloud_processing_consent"`
	}
	if !s.strict(w, r, &in) {
		return
	}
	if in.Enabled && (!in.Consent || s.compiled.Generator == nil) {
		s.automationRespond(w, r, 0, nil, automation.Invalid("server generation configuration and cloud processing consent required"))
		return
	}
	e := automation.Transaction(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, e := tx.Exec(r.Context(), `UPDATE tenants SET settings=settings||$2::jsonb WHERE id=$1`, tenant(r), automation.JSON(map[string]bool{"compiler_enabled": in.Enabled, "compiler_cloud_consent": in.Consent})); e != nil {
			return e
		}
		return automation.Audit(r.Context(), tx, tenant(r), s.principalName(r), "compiler.configured", tenant(r), nil, in)
	})
	s.automationRespond(w, r, 200, in, e)
}
func automationRange(r *http.Request, days int) (time.Time, time.Time, *time.Location, error) {
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		tz = "UTC"
	}
	z, e := time.LoadLocation(tz)
	if e != nil {
		return time.Time{}, time.Time{}, nil, automation.Invalid("invalid timezone")
	}
	now := time.Now()
	local := now.In(z)
	from := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, z).AddDate(0, 0, -days+1)
	to := now
	for _, p := range []string{"from", "to"} {
		if v := r.URL.Query().Get(p); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return from, to, z, automation.Invalid("invalid " + p)
			}
			if p == "from" {
				from = t
			} else {
				to = t
			}
		}
	}
	if !from.Before(to) || to.Sub(from) > time.Duration(days)*24*time.Hour+2*time.Hour {
		return from, to, z, automation.Invalid("invalid time range")
	}
	return from.UTC(), to.UTC(), z, nil
}
