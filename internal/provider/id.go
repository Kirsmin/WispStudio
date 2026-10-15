package provider

import (
	"fmt"
	"strings"
)

// ModelFullID 是模型全称 id，格式 "{ProviderID}/{ModelID}"，如 "deepseek/deepseek-chat"。
type ModelFullID string

// Parse 解析并校验全称 id 字符串；格式不符时返回错误。
func Parse(s string) (ModelFullID, error) {
	m := ModelFullID(s)
	providerID, modelID := m.Split()
	if providerID == "" || modelID == "" {
		return "", fmt.Errorf("provider: 模型全称 id 格式应为 {ProviderID}/{ModelID}: %q", s)
	}
	return m, nil
}

// Split 把全称 id 拆成 ProviderID 与 ModelID；格式不符时两部分均为空字符串。
func (m ModelFullID) Split() (providerID, modelID string) {
	s := string(m)
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", ""
	}
	providerID, modelID = s[:i], s[i+1:]
	if strings.Contains(modelID, "/") { // ModelID 本身不允许含 "/"
		return "", ""
	}
	return providerID, modelID
}

// ProviderID 返回全称 id 的 provider 部分；格式不符时返回空字符串。
func (m ModelFullID) ProviderID() string {
	providerID, _ := m.Split()
	return providerID
}

// ModelID 返回全称 id 的 model 部分；格式不符时返回空字符串。
func (m ModelFullID) ModelID() string {
	_, modelID := m.Split()
	return modelID
}

// Join 把 ProviderID 与 ModelID 拼接为全称 id。
func Join(providerID, modelID string) ModelFullID {
	return ModelFullID(providerID + "/" + modelID)
}

// String 返回字符串形式。
func (m ModelFullID) String() string { return string(m) }
