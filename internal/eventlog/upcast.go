package eventlog

import (
	"encoding/json"
	"fmt"
)

// Upcaster 事件版本升级器：把 v 版本的 attrs 升级到 v+1。
// 旧事件永不迁移落库，只在读取时逐版本升级。
type Upcaster interface {
	Upcast(v int, attrs json.RawMessage) (json.RawMessage, error)
}

// Registry 按事件类型注册升级器。
type Registry map[string]Upcaster

// upcast 把事件 attrs 按注册表逐版本升级到 CurrentVersion。
func (s *Store) upcast(ev *Event) error {
	if ev.V == CurrentVersion {
		return nil
	}
	if ev.V > CurrentVersion {
		return fmt.Errorf("eventlog: 事件 %s 版本 v%d 高于当前 v%d，无法读取", ev.ID, ev.V, CurrentVersion)
	}
	for ev.V < CurrentVersion {
		u, ok := s.reg[ev.Type]
		if !ok {
			return fmt.Errorf("eventlog: 事件类型 %q 缺少 v%d 的升级器", ev.Type, ev.V)
		}
		attrs, err := u.Upcast(ev.V, ev.Attrs)
		if err != nil {
			return fmt.Errorf("eventlog: 升级事件 %s (v%d→v%d) 失败: %w", ev.ID, ev.V, ev.V+1, err)
		}
		ev.Attrs = attrs
		ev.V++
	}
	return nil
}
