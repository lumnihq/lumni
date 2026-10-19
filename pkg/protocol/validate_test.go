package protocol

import "testing"

func TestActionValidate(t *testing.T) {
	valid := ActionCheckInput{WorkID: "w", ActorID: "a", Operation: "subscription.cancel", Resources: []ResourceIntent{{ResourceRef: ResourceRef{Type: "subscription", ExternalID: "1"}, Mode: AccessWrite}}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.Resources[0].Mode = "BOGUS"
	if err := valid.Validate(); err == nil {
		t.Fatal("expected invalid mode")
	}
}
