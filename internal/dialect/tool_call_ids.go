package dialect

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// The Gemini conversion carries a function call's thought signature inside its
// call ID as "<baseID>_ts_<signature>", so the signature survives clients that
// only echo IDs back. Such IDs run to thousands of characters, while OpenAI
// caps call IDs at 64. Stripping is deterministic, so a call and its output
// still match after a mid-conversation switch.

const (
	embeddedThoughtSignatureSeparator = "_ts_"
	maxPortableToolCallIDLength       = 64
)

// StripEmbeddedThoughtSignatures shortens over-long tool call IDs that embed a
// thought signature, in Responses, Chat Completions and Anthropic Messages
// requests. Unchanged bodies are returned as the original bytes.
func StripEmbeddedThoughtSignatures(body []byte) ([]byte, bool, error) {
	if !bytes.Contains(body, []byte(embeddedThoughtSignatureSeparator)) {
		return body, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var request any
	if err := decoder.Decode(&request); err != nil {
		return nil, false, fmt.Errorf("decode request")
	}
	if !stripToolCallIDs(request) {
		return body, false, nil
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, false, fmt.Errorf("encode request")
	}
	return encoded, true, nil
}

// stripToolCallIDs rewrites protocol tool-call IDs and the references that
// point back at them. Tool arguments, tool inputs, and tool outputs are user
// data and are left unchanged.
func stripToolCallIDs(value any) bool {
	changed := false
	switch typed := value.(type) {
	case map[string]any:
		changed = rewriteProtocolToolCallIDs(typed)
		for key, child := range typed {
			if toolPayloadKey(typed, key) {
				continue
			}
			changed = stripToolCallIDs(child) || changed
		}
	case []any:
		for _, child := range typed {
			changed = stripToolCallIDs(child) || changed
		}
	}
	return changed
}

func rewriteProtocolToolCallIDs(object map[string]any) bool {
	changed := false
	if id, ok := object["call_id"]; ok && objectCarriesProtocolCallID(object) {
		if stripped, ok := strippedToolCallID(id); ok {
			object["call_id"] = stripped
			changed = true
		}
	}
	if id, ok := object["tool_use_id"]; ok && isToolResultObject(object) {
		if stripped, ok := strippedToolCallID(id); ok {
			object["tool_use_id"] = stripped
			changed = true
		}
	}
	if id, ok := object["tool_call_id"]; ok && isChatToolMessage(object) {
		if stripped, ok := strippedToolCallID(id); ok {
			object["tool_call_id"] = stripped
			changed = true
		}
	}
	if id, ok := object["id"]; ok && isToolCallObject(object) {
		if stripped, ok := strippedToolCallID(id); ok {
			object["id"] = stripped
			changed = true
		}
	}
	return changed
}

func objectCarriesProtocolCallID(object map[string]any) bool {
	kind, _ := object["type"].(string)
	switch kind {
	case "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output",
		"computer_call", "computer_call_output", "local_shell_call", "local_shell_call_output",
		"apply_patch_call", "apply_patch_call_output", "shell_call", "shell_call_output":
		return true
	default:
		return false
	}
}

func isToolResultObject(object map[string]any) bool {
	kind, _ := object["type"].(string)
	return kind == "tool_result"
}

func isChatToolMessage(object map[string]any) bool {
	role, _ := object["role"].(string)
	return role == "tool"
}

// toolPayloadKey reports fields that carry tool arguments or results rather
// than another protocol message. Those values are not walked.
func toolPayloadKey(object map[string]any, key string) bool {
	switch key {
	case "arguments":
		return true
	case "input":
		kind, _ := object["type"].(string)
		switch kind {
		case "tool_use", "server_tool_use", "custom_tool_call":
			return true
		}
	case "output":
		kind, _ := object["type"].(string)
		if kind == "function_call_output" || strings.HasSuffix(kind, "_output") {
			return true
		}
	case "content":
		kind, _ := object["type"].(string)
		if kind == "tool_result" {
			return true
		}
	}
	return false
}

// isToolCallObject reports whether an object is a tool call whose id is the
// call ID: a Chat Completions tool call or an Anthropic tool_use block.
func isToolCallObject(object map[string]any) bool {
	if _, hasFunction := object["function"]; hasFunction {
		return true
	}
	kind, _ := object["type"].(string)
	switch kind {
	case "tool_use", "server_tool_use", "function_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

func strippedToolCallID(value any) (string, bool) {
	id, ok := value.(string)
	if !ok || len(id) <= maxPortableToolCallIDLength {
		return "", false
	}
	base, _, found := strings.Cut(id, embeddedThoughtSignatureSeparator)
	if !found || base == "" {
		return "", false
	}
	if len(base) <= maxPortableToolCallIDLength {
		return base, true
	}
	return fitToolCallID(base), true
}

// fitToolCallID shortens a base that is itself over the upstream limit. The
// digest is of the base only, so a call and its output stay paired whether or
// not one of them still carries the thought-signature suffix.
func fitToolCallID(base string) string {
	sum := sha256.Sum256([]byte(base))
	return "id_" + hex.EncodeToString(sum[:16])
}
