package main

import (
	"context"
	"encoding/json"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"log"
	adapter "lumni/adapters/temporal"
	"lumni/pkg/protocol"
	lumni "lumni/sdk/go"
	"os"
)

func agent(ctx workflow.Context, input protocol.ActionCheckInput) (json.RawMessage, error) {
	return adapter.Execute(ctx, "echo", input)
}
func main() {
	host := os.Getenv("TEMPORAL_ADDRESS")
	if host == "" {
		host = client.DefaultHostPort
	}
	connection, err := client.Dial(client.Options{HostPort: host})
	if err != nil {
		log.Fatal(err)
	}
	defer connection.Close()
	url := os.Getenv("LUMNI_URL")
	if url == "" {
		url = "http://localhost:8080"
	}
	w := worker.New(connection, "lumni-agent", worker.Options{})
	w.RegisterWorkflow(agent)
	w.RegisterActivity(&adapter.Activities{Client: lumni.New(url, os.Getenv("LUMNI_API_KEY")), Tools: map[string]func(context.Context, json.RawMessage) (json.RawMessage, error){"echo": func(_ context.Context, input json.RawMessage) (json.RawMessage, error) { return input, nil }}})
	log.Fatal(w.Run(worker.InterruptCh()))
}
