package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEventsAreJSONLAndNeverLogSecretValues(t *testing.T) {
	var buf bytes.Buffer
	l := NewWriter(&buf)

	l.Egress("api.github.com", "CONNECT", true)
	l.Egress("evil.example.com", "CONNECT", false)
	l.MCP("github", "tools/call", "1")
	l.Secret("gh-token", "github") // 只记名字

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("应有 4 行 JSONL，实际 %d", len(lines))
	}

	// 每行都是合法 JSON。
	for _, ln := range lines {
		var e Event
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			t.Fatalf("非法 JSON: %s", ln)
		}
		if e.Time == "" {
			t.Errorf("缺少时间戳: %s", ln)
		}
	}

	// 放行/拒绝判定正确。
	if !strings.Contains(lines[0], `"decision":"allow"`) || !strings.Contains(lines[1], `"decision":"deny"`) {
		t.Errorf("出网判定记录错误:\n%s\n%s", lines[0], lines[1])
	}
	// 密钥事件只含名字，绝不含值。
	if !strings.Contains(lines[3], `"secret":"gh-token"`) {
		t.Errorf("密钥名未记录: %s", lines[3])
	}
}

func TestNilLoggerIsSafe(t *testing.T) {
	var l *Logger
	l.Egress("x", "GET", true) // 不应 panic
	l.MCP("s", "m", "1")
}
