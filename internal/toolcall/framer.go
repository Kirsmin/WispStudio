package toolcall

import (
	"fmt"
	"strings"
)

const marker = "<tc:"

type Call struct {
	Name string
	Body string
	Raw  string
}

type Result struct {
	Text     string
	Detected bool
	Call     *Call
	Err      error
}

// Framer 只识别 Wisp 的 <tc:Name /> / <tc:Name>body</tc:Name>。
// 它不是 XML parser：body 是 opaque text，也不支持 nested tool call。
type Framer struct {
	buf      string
	inTool   bool
	done     bool
	detected bool
}

func (f *Framer) Feed(chunk string) Result {
	if f.done || chunk == "" {
		return Result{}
	}
	f.buf += chunk
	var out strings.Builder
	result := Result{}

	for !f.inTool {
		pos := strings.IndexByte(f.buf, '<')
		if pos < 0 {
			out.WriteString(f.buf)
			f.buf = ""
			result.Text = out.String()
			return result
		}
		if pos > 0 {
			out.WriteString(f.buf[:pos])
			f.buf = f.buf[pos:]
		}

		// <、<t、<tc 都可能是跨 chunk 的 marker，先 hold。
		if strings.HasPrefix(marker, f.buf) {
			result.Text = out.String()
			return result
		}
		if strings.HasPrefix(f.buf, marker) {
			f.inTool = true
			if !f.detected {
				f.detected = true
				result.Detected = true
			}
			break
		}

		// 已确定不是 <tc:，只放行 '<'，继续扫描剩余内容。
		out.WriteByte('<')
		f.buf = f.buf[1:]
	}

	call, complete, err := parseCall(f.buf)
	if err != nil {
		f.done = true
		result.Text = out.String()
		result.Err = err
		return result
	}
	if complete {
		f.done = true
		result.Text = out.String()
		result.Call = call
		return result
	}
	result.Text = out.String()
	return result
}

func (f *Framer) Finalize() Result {
	if f.done {
		return Result{}
	}
	f.done = true
	if f.inTool {
		return Result{Err: fmt.Errorf("incomplete tool call")}
	}
	text := f.buf
	f.buf = ""
	return Result{Text: text}
}

func parseCall(raw string) (*Call, bool, error) {
	if !strings.HasPrefix(raw, marker) {
		return nil, false, fmt.Errorf("invalid tool call prefix")
	}
	openEnd := strings.IndexByte(raw, '>')
	if openEnd < 0 {
		return nil, false, nil
	}
	rawInside := raw[len(marker):openEnd]
	// 工具名必须紧跟 tc:，避免把 <tc: Time /> 也悄悄接受。
	if rawInside == "" || rawInside[0] == ' ' || rawInside[0] == '\t' || rawInside[0] == '\r' || rawInside[0] == '\n' {
		return nil, false, fmt.Errorf("tool name must immediately follow tc:")
	}
	inside := strings.TrimSpace(rawInside)
	if inside == "" {
		return nil, false, fmt.Errorf("empty tool name")
	}
	if strings.HasSuffix(inside, "/") {
		name := strings.TrimSpace(strings.TrimSuffix(inside, "/"))
		if !validName(name) {
			return nil, false, fmt.Errorf("invalid tool name %q", name)
		}
		return &Call{Name: name, Raw: raw[:openEnd+1]}, true, nil
	}
	name := inside
	if !validName(name) {
		return nil, false, fmt.Errorf("invalid tool name %q", name)
	}
	closing := "</tc:" + name + ">"
	closeAt := strings.Index(raw[openEnd+1:], closing)
	if closeAt < 0 {
		return nil, false, nil
	}
	closeAt += openEnd + 1
	end := closeAt + len(closing)
	return &Call{
		Name: name,
		Body: raw[openEnd+1 : closeAt],
		Raw:  raw[:end],
	}, true, nil
}

func validName(name string) bool {
	if name == "" || !isAlpha(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if !isAlpha(c) && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
