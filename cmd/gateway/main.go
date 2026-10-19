package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"lumni/internal/gateway"
	"lumni/pkg/protocol"
	lumni "lumni/sdk/go"
)

func main() {
	mode := flag.String("transport", "stdio", "stdio, http (MCP), openapi, a2a, or model")
	openapi := flag.String("openapi", "", "OpenAPI JSON document for HTTP route discovery")
	policy := flag.String("mode", env("LUMNI_GATEWAY_MODE", ""), "SHADOW, ENFORCE, or empty to follow the service configuration")
	listen := flag.String("listen", ":8090", "HTTP listen address")
	upstream := flag.String("upstream", "", "HTTP upstream URL")
	command := flag.String("command", "", "stdio upstream command")
	flag.Parse()
	client := lumni.New(env("LUMNI_URL", "http://localhost:8080"), os.Getenv("LUMNI_API_KEY"))
	proxy := gateway.New(gateway.Config{Client: client, ActorID: env("LUMNI_GATEWAY_ACTOR", "mcp-agent"), SessionID: env("LUMNI_GATEWAY_SESSION", "mcp-session"), WorkID: env("LUMNI_GATEWAY_WORK", "mcp-work"), Risk: protocol.RiskLevel(strings.ToUpper(env("LUMNI_GATEWAY_RISK", "MEDIUM"))), Mode: protocol.EnforcementMode(strings.ToUpper(*policy)), ManagedPolicy: *policy == "", FailClosed: env("LUMNI_GATEWAY_FAIL_CLOSED", "false") == "true"})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *mode == "stdio" {
		if err := proxy.Stdio(ctx, *command, flag.Args()...); err != nil {
			log.Fatal(err)
		}
		return
	}
	var handler http.Handler
	var err error
	switch *mode {
	case "http":
		handler, err = proxy.HTTPHandler(*upstream)
	case "a2a":
		handler, err = proxy.A2AHandler(*upstream)
	case "model":
		handler, err = proxy.ModelHandler(*upstream)
	case "openapi":
		var data []byte
		data, err = os.ReadFile(*openapi)
		if err == nil {
			var routes []gateway.HTTPRoute
			routes, err = gateway.OpenAPIRoutes(data)
			if err == nil {
				handler, err = proxy.RESTHandler(*upstream, routes)
			}
		}
	default:
		log.Fatal("unsupported transport")
	}
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: *listen, Handler: handler}
	go func() { <-ctx.Done(); _ = server.Shutdown(context.Background()) }()
	log.Printf("Lumni MCP gateway listening on %s", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
