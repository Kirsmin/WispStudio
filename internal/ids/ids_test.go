package ids

import "testing"

// 前缀 → 实体类型的完整映射表，覆盖全部实体 ID。
var prefixWant = []struct {
	prefix string
	kind   Kind
}{
	{"evt", KindEvent},
	{"msg", KindMessage},
	{"tc", KindToolCall},
	{"pr", KindPrompt},
	{"req", KindRequest},
	{"ctx", KindContext},
	{"op", KindContextOp},
	{"ag", KindAgent},
	{"ck", KindCheckpoint},
	{"tn", KindTurn},
	{"sess", KindSession},
}

// TestParse 校验每个前缀都能解析到正确的实体类型（最长前缀优先）。
func TestParse(t *testing.T) {
	for _, tt := range prefixWant {
		kind, err := Parse(tt.prefix + "_01J8ZP9W4RYY7Q0K3F6N5M2B1VC")
		if err != nil {
			t.Fatalf("Parse(%q) 出错: %v", tt.prefix, err)
		}
		if kind != tt.kind {
			t.Errorf("Parse(%q) = %v, 期望 %v", tt.prefix, kind, tt.kind)
		}
	}
}

// TestParseUnknown 校验未知前缀报错。
func TestParseUnknown(t *testing.T) {
	for _, s := range []string{"", "xyz_01J8Z", "msg", "msg-", "MSG_01J8Z"} {
		if kind, err := Parse(s); err == nil || kind != KindUnknown {
			t.Errorf("Parse(%q) 应报错并返回 KindUnknown, 得到 kind=%v err=%v", s, kind, err)
		}
	}
}

// TestNewAndParseRoundTrip 校验各类型构造器生成的 ID 能被全局 Parse 与各类型 Parse 正确识别。
func TestNewAndParseRoundTrip(t *testing.T) {
	eventID := NewEventID()
	if kind, err := Parse(eventID.String()); err != nil || kind != KindEvent {
		t.Errorf("Parse(%s) = %v, %v", eventID, kind, err)
	}
	if _, err := ParseEventID(eventID.String()); err != nil {
		t.Errorf("ParseEventID(%s) 出错: %v", eventID, err)
	}
	if _, err := ParseMessageID(eventID.String()); err == nil {
		t.Errorf("ParseMessageID(%s) 应因前缀不符报错", eventID)
	}

	msgID := NewMessageID()
	if _, err := ParseMessageID(msgID.String()); err != nil {
		t.Errorf("ParseMessageID(%s) 出错: %v", msgID, err)
	}

	toolCallID := NewToolCallID()
	if kind, err := Parse(toolCallID.String()); err != nil || kind != KindToolCall {
		t.Errorf("Parse(%s) = %v, %v", toolCallID, kind, err)
	}

	promptID := NewPromptID()
	if kind, err := Parse(promptID.String()); err != nil || kind != KindPrompt {
		t.Errorf("Parse(%s) = %v, %v", promptID, kind, err)
	}

	reqID := NewRequestID()
	if kind, err := Parse(reqID.String()); err != nil || kind != KindRequest {
		t.Errorf("Parse(%s) = %v, %v", reqID, kind, err)
	}

	ctxID := NewContextID()
	if kind, err := Parse(ctxID.String()); err != nil || kind != KindContext {
		t.Errorf("Parse(%s) = %v, %v", ctxID, kind, err)
	}

	opID := NewContextOpID()
	if kind, err := Parse(opID.String()); err != nil || kind != KindContextOp {
		t.Errorf("Parse(%s) = %v, %v", opID, kind, err)
	}

	agentID := NewAgentID()
	if kind, err := Parse(agentID.String()); err != nil || kind != KindAgent {
		t.Errorf("Parse(%s) = %v, %v", agentID, kind, err)
	}

	checkpointID := NewCheckpointID()
	if kind, err := Parse(checkpointID.String()); err != nil || kind != KindCheckpoint {
		t.Errorf("Parse(%s) = %v, %v", checkpointID, kind, err)
	}

	turnID := NewTurnID()
	if kind, err := Parse(turnID.String()); err != nil || kind != KindTurn {
		t.Errorf("Parse(%s) = %v, %v", turnID, kind, err)
	}

	sessionID := NewSessionID()
	if kind, err := Parse(sessionID.String()); err != nil || kind != KindSession {
		t.Errorf("Parse(%s) = %v, %v", sessionID, kind, err)
	}
	if _, err := ParseSessionID(sessionID.String()); err != nil {
		t.Errorf("ParseSessionID(%s) 出错: %v", sessionID, err)
	}
	if _, err := ParseSessionID(turnID.String()); err == nil {
		t.Errorf("ParseSessionID(%s) 应因前缀不符报错", turnID)
	}
}

// TestParseLongestPrefix 校验 "req" 等较长前缀不被 "pr" 等短前缀抢先匹配。
func TestParseLongestPrefix(t *testing.T) {
	kind, err := Parse("req_01J8ZP9W4RYY7Q0K3F6N5M2B1VC")
	if err != nil || kind != KindRequest {
		t.Errorf("Parse(req_...) = %v, %v, 期望 KindRequest（最长前缀优先）", kind, err)
	}
}
