package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/lumnihq/lumni/pkg/protocol"
	lumni "github.com/lumnihq/lumni/sdk/go"
)

func main() {
	ctx := context.Background()
	client := lumni.New(env("LUMNI_URL", "http://localhost:8080"), env("LUMNI_API_KEY", "lumni_local_change_me"))
	_, _ = client.PutActor(ctx, protocol.Actor{ID: "go-example", Type: protocol.ActorAgent, Runtime: "go"})
	work, err := client.StartWork(ctx, protocol.Work{ID: "go-example-work", ActorID: "go-example", Goal: "Safely cancel a subscription", Risk: protocol.RiskHigh})
	if err != nil {
		panic(err)
	}
	version := "20"
	key := "cancel:sub_91"
	decision, err := client.CheckAction(ctx, protocol.ActionCheckInput{WorkID: work.ID, ActorID: work.ActorID, Operation: "subscription.cancel", Arguments: json.RawMessage(`{"subscription_id":"sub_91"}`), Resources: []protocol.ResourceIntent{{ResourceRef: protocol.ResourceRef{Type: "subscription", ExternalID: "sub_91"}, Mode: protocol.AccessWrite, ObservedVersion: &version, VersionSource: "etag"}}, IdempotencyKey: &key, Risk: protocol.RiskHigh})
	if err != nil {
		panic(err)
	}
	fmt.Printf("decision=%s reason=%s enforced=%t\n", decision.Decision, decision.ReasonCode, decision.Enforced)
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
