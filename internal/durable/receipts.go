package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"lumni/internal/auth"
)

// A response may be retried after the mutation released its work claim. Only
// the owner of that immutable receipt can retrieve it, with identical input.
func (s *Service) receipt(ctx context.Context, tx pgx.Tx, tenant, work, id, token string, input, out any) (bool, error) {
	b, _ := json.Marshal(input)
	var owner, response []byte
	var hash string
	err := tx.QueryRow(ctx, `SELECT owner_hash,input_hash,response_ciphertext FROM runtime_receipts WHERE tenant_id=$1 AND work_id=$2 AND identity=$3`, tenant, work, id).Scan(&owner, &hash, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Equal(owner, auth.Hash(token)) || hash != digest(b) {
		return false, errors.New("RETRY_IDENTITY_MISMATCH")
	}
	if len(response) == 0 {
		return false, errors.New("REPLAY_DATA_UNAVAILABLE")
	}
	data, err := s.cipher.Decrypt(response)
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(data, out)
}
func (s *Service) saveReceipt(ctx context.Context, tx pgx.Tx, tenant, work, id, token string, input, out any) error {
	b, _ := json.Marshal(input)
	r, _ := json.Marshal(out)
	cipher, err := s.cipher.Encrypt(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO runtime_receipts(tenant_id,work_id,identity,owner_hash,input_hash,response_ciphertext)VALUES($1,$2,$3,$4,$5,$6)`, tenant, work, id, auth.Hash(token), digest(b), cipher)
	return err
}
