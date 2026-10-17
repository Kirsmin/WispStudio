package ids

import "testing"

// TestIDRoundTrip 校验强类型 ID 的构造与前缀解析。
func TestIDRoundTrip(t *testing.T) {
	eventID := NewEventID()
	if _, err := ParseEventID(eventID.String()); err != nil {
		t.Errorf("ParseEventID(%s) 出错: %v", eventID, err)
	}
	sessionID := NewSessionID()
	if _, err := ParseSessionID(sessionID.String()); err != nil {
		t.Errorf("ParseSessionID(%s) 出错: %v", sessionID, err)
	}
	toolCallID := NewToolCallID()
	if _, err := ParseToolCallID(toolCallID.String()); err != nil {
		t.Errorf("ParseToolCallID(%s) 出错: %v", toolCallID, err)
	}
	if _, err := ParseSessionID(eventID.String()); err == nil {
		t.Errorf("ParseSessionID(%s) 应因前缀不符报错", eventID)
	}
}
