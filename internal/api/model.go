package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func payloadHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (s *Server) modelCall(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       string          `json:"id"`
		WorkID   string          `json:"work_id"`
		StepID   *string         `json:"step_id"`
		Provider string          `json:"provider"`
		Model    string          `json:"model"`
		Input    json.RawMessage `json:"input"`
		Output   json.RawMessage `json:"output"`
		Status   string          `json:"status"`
		Usage    json.RawMessage `json:"usage"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.ID == "" || in.WorkID == "" || !json.Valid(in.Input) || (in.Status != "PENDING" && in.Status != "COMPLETE" && in.Status != "INCOMPLETE") {
		s.respond(w, r, 0, nil, errors.New("id, work, input and valid model result status required"))
		return
	}
	input, e := s.secrets.Encrypt(in.Input)
	if e != nil {
		s.respond(w, r, 0, nil, e)
		return
	}
	var output []byte
	if in.Status == "COMPLETE" {
		if !json.Valid(in.Output) {
			s.respond(w, r, 0, nil, errors.New("complete model result is required"))
			return
		}
		output, e = s.secrets.Encrypt(in.Output)
		if e != nil {
			s.respond(w, r, 0, nil, e)
			return
		}
	}
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		s.respond(w, r, 0, nil, e)
		return
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenant(r)+":model:"+in.ID); e != nil {
		s.respond(w, r, 0, nil, e)
		return
	}
	created := false
	var oldHash, oldStatus, oldOutput string
	var sameIdentity bool
	e = tx.QueryRow(r.Context(), `SELECT input_hash,status,output_hash,(work_id=$3 AND step_id IS NOT DISTINCT FROM $4 AND provider=$5 AND model=$6) FROM model_calls WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant(r), in.ID, in.WorkID, in.StepID, in.Provider, in.Model).Scan(&oldHash, &oldStatus, &oldOutput, &sameIdentity)
	if e == nil {
		if !sameIdentity || oldHash != payloadHash(in.Input) || (in.Status != "PENDING" && oldStatus != "PENDING" && (oldStatus != in.Status || oldOutput != payloadHash(in.Output))) {
			s.respond(w, r, 0, nil, errors.New("MODEL_CALL_IDENTITY_MISMATCH"))
			return
		}
		if in.Status == "PENDING" {
			s.respond(w, r, 200, map[string]any{"created": false, "status": oldStatus}, nil)
			return
		}
	} else if !errors.Is(e, pgx.ErrNoRows) {
		s.respond(w, r, 0, nil, e)
		return
	}
	created = errors.Is(e, pgx.ErrNoRows)
	_, e = tx.Exec(r.Context(), `INSERT INTO model_calls(tenant_id,id,work_id,step_id,provider,model,status,input_hash,input_ciphertext,output_hash,output_ciphertext,usage,completed_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,CASE WHEN $7<>'PENDING' THEN now() END)ON CONFLICT(tenant_id,id)DO UPDATE SET status=EXCLUDED.status,output_hash=EXCLUDED.output_hash,output_ciphertext=EXCLUDED.output_ciphertext,usage=EXCLUDED.usage,completed_at=EXCLUDED.completed_at WHERE model_calls.status='PENDING'`, tenant(r), in.ID, in.WorkID, in.StepID, in.Provider, in.Model, in.Status, payloadHash(in.Input), input, payloadHash(in.Output), output, rawJSON(in.Usage, "{}"))
	if e == nil {
		e = tx.Commit(r.Context())
	}
	s.respond(w, r, 200, map[string]any{"recorded": e == nil, "created": created, "status": in.Status}, e)
}
func (s *Server) modelPayload(w http.ResponseWriter, r *http.Request) {
	var cipher []byte
	err := s.pool.QueryRow(r.Context(), `SELECT output_ciphertext FROM model_calls WHERE tenant_id=$1 AND id=$2 AND status='COMPLETE'`, tenant(r), chi.URLParam(r, "id")).Scan(&cipher)
	var data []byte
	if err == nil {
		if len(cipher) == 0 {
			err = errors.New("REPLAY_DATA_UNAVAILABLE")
		} else {
			data, err = s.secrets.Decrypt(cipher)
		}
	}
	s.respond(w, r, 200, map[string]any{"output": json.RawMessage(data)}, err)
}
