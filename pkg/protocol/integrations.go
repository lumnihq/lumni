package protocol

import "encoding/json"

type ResourceMapping struct {
	Mode                   AccessMode `json:"mode"`
	Type                   string     `json:"type"`
	Namespace              string     `json:"namespace,omitempty"`
	IDPointer              string     `json:"id_pointer"`
	ObservedVersionPointer string     `json:"observed_version_pointer,omitempty"`
	VersionSource          string     `json:"version_source,omitempty"`
}
type ToolMapping struct {
	Source          string            `json:"source"`
	Tool            string            `json:"tool"`
	Operation       string            `json:"operation"`
	Resources       []ResourceMapping `json:"resources"`
	EffectKeyFields []string          `json:"effect_key_fields,omitempty"`
}
type ReconciliationHandler struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type ReconciliationInput struct {
	ID             string          `json:"id"`
	OutcomeID      string          `json:"outcome_id"`
	Handler        string          `json:"handler"`
	HandlerVersion string          `json:"handler_version"`
	Input          json.RawMessage `json:"input,omitempty"`
}
type EffectIdentityResolution struct {
	LegacyKey  string          `json:"legacy_key"`
	CurrentKey string          `json:"current_key"`
	Evidence   json.RawMessage `json:"evidence"`
}
