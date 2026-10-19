package gateway

import (
	"encoding/json"
	"errors"
	"sort"
)

// assembleChat reconstructs the complete provider result from ordered SSE deltas,
// including fragmented function names/arguments and parallel tool-call indexes.
func assembleChat(events []json.RawMessage) (json.RawMessage, error) {
	result := map[string]any{"object": "chat.completion"}
	choices := map[int]map[string]any{}
	tools := map[int]map[int]map[string]any{}
	for _, raw := range events {
		var event map[string]any
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		for _, key := range []string{"id", "created", "model", "system_fingerprint", "usage"} {
			if v, ok := event[key]; ok && v != nil {
				result[key] = v
			}
		}
		list, _ := event["choices"].([]any)
		for _, item := range list {
			ch, _ := item.(map[string]any)
			number, ok := ch["index"].(float64)
			if !ok {
				return nil, errors.New("choice index missing")
			}
			index := int(number)
			choice := choices[index]
			if choice == nil {
				choice = map[string]any{"index": index, "message": map[string]any{"role": "assistant", "content": ""}}
				choices[index] = choice
			}
			message := choice["message"].(map[string]any)
			if finish := ch["finish_reason"]; finish != nil {
				choice["finish_reason"] = finish
			}
			delta, _ := ch["delta"].(map[string]any)
			for _, key := range []string{"role", "content", "refusal"} {
				if value, ok := delta[key].(string); ok {
					if key == "role" {
						message[key] = value
					} else {
						previous, _ := message[key].(string)
						message[key] = previous + value
					}
				}
			}
			calls, _ := delta["tool_calls"].([]any)
			for _, call := range calls {
				entry, _ := call.(map[string]any)
				n, _ := entry["index"].(float64)
				if tools[index] == nil {
					tools[index] = map[int]map[string]any{}
				}
				t := tools[index][int(n)]
				if t == nil {
					t = map[string]any{"type": "function", "function": map[string]any{"name": "", "arguments": ""}}
					tools[index][int(n)] = t
				}
				if id, ok := entry["id"].(string); ok {
					t["id"] = id
				}
				fn, _ := entry["function"].(map[string]any)
				out := t["function"].(map[string]any)
				for _, key := range []string{"name", "arguments"} {
					if v, ok := fn[key].(string); ok {
						old, _ := out[key].(string)
						out[key] = old + v
					}
				}
			}
		}
	}
	if len(choices) == 0 {
		return nil, errors.New("chat completed without choices")
	}
	indexes := []int{}
	for i := range choices {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	out := []any{}
	for _, i := range indexes {
		choice := choices[i]
		if choice["finish_reason"] == nil {
			return nil, errors.New("chat choice missing finish reason")
		}
		if len(tools[i]) > 0 {
			ids := []int{}
			for id := range tools[i] {
				ids = append(ids, id)
			}
			sort.Ints(ids)
			calls := []any{}
			for _, id := range ids {
				calls = append(calls, tools[i][id])
			}
			choice["message"].(map[string]any)["tool_calls"] = calls
		}
		out = append(out, choice)
	}
	result["choices"] = out
	return json.Marshal(result)
}
