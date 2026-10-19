package main

import (
	"context"
	"encoding/json"
	adapter "github.com/lumnihq/lumni/adapters/restate"
	lumni "github.com/lumnihq/lumni/sdk/go"
	sdk "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"
	"log"
	"os"
)

func main() {
	url := os.Getenv("LUMNI_URL")
	if url == "" {
		url = "http://localhost:8080"
	}
	handler := &adapter.Handler{Client: lumni.New(url, os.Getenv("LUMNI_API_KEY")), Tools: map[string]func(context.Context, json.RawMessage) (json.RawMessage, error){"echo": func(_ context.Context, input json.RawMessage) (json.RawMessage, error) { return input, nil }}}
	address := os.Getenv("RESTATE_HANDLER_ADDR")
	if address == "" {
		address = ":9080"
	}
	log.Fatal(server.NewRestate().Bind(sdk.Reflect(handler)).Start(context.Background(), address))
}
