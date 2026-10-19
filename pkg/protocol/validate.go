package protocol

import (
	"errors"
	"fmt"
	"strings"
)

func (in ActionCheckInput) Validate() error {
	if strings.TrimSpace(in.WorkID) == "" || strings.TrimSpace(in.ActorID) == "" {
		return errors.New("work_id and actor_id are required")
	}
	if strings.TrimSpace(in.Operation) == "" {
		return errors.New("operation is required")
	}
	if len(in.Resources) == 0 && in.Source == "" {
		return errors.New("at least one resolved resource is required")
	}
	for i, r := range in.Resources {
		if r.ID == "" && (r.Type == "" || r.ExternalID == "") {
			return fmt.Errorf("resources[%d] requires id or type and external_id", i)
		}
		switch r.Mode {
		case AccessObserve, AccessRead, AccessWrite, AccessCreate, AccessDelete, AccessImpact:
		default:
			return fmt.Errorf("resources[%d] has invalid mode %q", i, r.Mode)
		}
	}
	if in.Risk == "" {
		in.Risk = RiskMedium
	}
	return nil
}

func IsWrite(mode AccessMode) bool {
	return mode == AccessWrite || mode == AccessCreate || mode == AccessDelete
}
