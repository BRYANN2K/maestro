package agentcore

import (
	"encoding/json"
	"strings"
)

// apiErrorLimit caps a raw error body so a hostile or verbose provider
// cannot flood the chat transcript.
const apiErrorLimit = 300

// parseAPIError turns a non-2xx provider response into a clean, one-line
// message. OpenAI-compatible APIs return {"error":{"message","type","code"}}
// while Anthropic returns {"type":"error","error":{"type","message"}};
// anything else falls back to the raw text (truncated).
func parseAPIError(status string, body []byte) string {
	msg := extractErrorJSON(body)
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	msg = strings.Join(strings.Fields(msg), " ") // collapse newlines/controls
	msg = truncateError(msg)
	if msg == "" {
		msg = "empty error body"
	}
	return status + ": " + msg
}

// extractErrorJSON pulls the human-readable message from the error bodies
// used by the major chat-completions APIs.
func extractErrorJSON(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var root struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &root); err == nil && root.Error.Message != "" {
		msg := root.Error.Message
		if root.Error.Code != nil {
			if code := compactCode(root.Error.Code); code != "" && !strings.Contains(msg, code) {
				msg += " (" + code + ")"
			}
		}
		return msg
	}
	return ""
}

// compactCode renders a JSON code value (string or number) compactly.
func compactCode(code any) string {
	switch v := code.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		if v == float64(int64(v)) {
			return itoa(int64(v))
		}
	}
	return ""
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// truncateError keeps the meaningful head of an error message.
func truncateError(s string) string {
	runes := []rune(s)
	if len(runes) <= apiErrorLimit {
		return s
	}
	return string(runes[:apiErrorLimit]) + "…"
}
