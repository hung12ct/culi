package llmgen

import (
	"encoding/json"
	"fmt"
	"strings"
)

// schemaInstruction renders the JSON-schema demand appended to prompts for
// backends without server-side schema enforcement (claude CLI). Kept blunt:
// the anthropic path proved models still fence JSON when asked not to, so
// decodeLooseJSON tolerates it anyway.
func schemaInstruction(schema map[string]any) string {
	raw, err := json.Marshal(schema)
	if err != nil {
		return "\n\nRespond with ONLY a JSON object. No prose, no code fences."
	}
	return "\n\nRespond with ONLY a JSON object — no prose, no code fences — matching this JSON Schema exactly:\n" + string(raw)
}

// decodeLooseJSON extracts the first JSON object from model output that may
// be wrapped in code fences or stray prose, and decodes it into out.
func decodeLooseJSON(raw string, out any) error {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j > i {
			s = s[i : j+1]
		}
	}
	if err := json.Unmarshal([]byte(s), out); err != nil {
		return fmt.Errorf("llmgen: decoding model JSON: %w", err)
	}
	return nil
}
