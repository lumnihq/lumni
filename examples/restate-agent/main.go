package main

import (
	"context"
	"encoding/json"
	sdk "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"
	"log"
	adapter "lumni/adapters/restate"
	lumni "lumni/sdk/go"
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
