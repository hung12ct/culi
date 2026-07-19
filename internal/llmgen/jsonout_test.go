package llmgen

import "testing"

func TestDecodeLooseJSON(t *testing.T) {
	var out struct {
		A int `json:"a"`
	}
	for _, s := range []string{
		`{"a":1}`,
		"```json\n{\"a\":1}\n```",
		"Here you go:\n\n{\"a\":1}\n\nHope that helps!",
	} {
		out.A = 0
		if err := decodeLooseJSON(s, &out); err != nil || out.A != 1 {
			t.Errorf("decodeLooseJSON(%q) = %v, a=%d", s, err, out.A)
		}
	}
	if err := decodeLooseJSON("no json here", &out); err == nil {
		t.Error("want error for JSON-free text")
	}
}
