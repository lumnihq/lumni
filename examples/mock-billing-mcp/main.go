package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var input message
		if json.Unmarshal(scanner.Bytes(), &input) != nil {
			continue
		}
		switch input.Method {
		case "initialize":
			write(input.ID, map[string]any{
				"protocolVersion": "2026-07-28",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "mock-billing", "version": "0.1.0"},
			})
		case "tools/list":
			write(input.ID, map[string]any{"tools": []any{
				map[string]any{
					"name":        "cancel_subscription",
					"description": "Cancel a subscription",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"subscription_id": map[string]any{"type": "string"},
							"version":         map[string]any{"type": "string"},
						},
						"required": []string{"subscription_id"},
					},
				},
			}})
		case "tools/call":
			write(input.ID, map[string]any{
				"content":           []any{map[string]any{"type": "text", "text": "subscription mutation accepted"}},
				"structuredContent": map[string]any{"external_result_id": "mock_1", "version": "20"},
			})
		default:
			write(input.ID, map[string]any{})
		}
	}
}

func write(id json.RawMessage, result any) {
	value, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(value))
}
