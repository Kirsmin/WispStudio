package runtime

import "sort"

type AgentProfile struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"display_name"`
	Prompt           string   `json:"-"`
	Tools            []string `json:"tools"`
	ToolPolicy       string   `json:"tool_policy"`
	ContextPolicy    string   `json:"context_policy"`
	PermissionPolicy string   `json:"permission_policy"`
	TransitionPolicy string   `json:"transition_policy,omitempty"`
	IdlePolicy       string   `json:"idle_policy"`
}

type ContextPolicy struct {
	ID               string
	FullState        bool
	IncludeObjective bool
}

type PermissionPolicy struct {
	ID        string
	AskUser   map[string]string
	DenyTools map[string]string
}

type TransitionPolicy struct {
	ID                   string
	Command              string
	TargetProfile        string
	RequiredArtifactType string
	RequiredArtifactName string
	CheckpointKind       string
	Reason               string
}

type ProfileRegistry struct {
	profiles           map[string]AgentProfile
	toolPolicies       map[string][]string
	contextPolicies    map[string]ContextPolicy
	permissionPolicies map[string]PermissionPolicy
	transitions        map[string]TransitionPolicy
}

func NewProfileRegistry() *ProfileRegistry {
	r := &ProfileRegistry{
		profiles: map[string]AgentProfile{},
		toolPolicies: map[string][]string{
			"planning": {"list_files", "read_file", "search_text", "spawn_explorer", "new_plan", "edit_plan"},
			// Phase 的创建与更新由 Runtime 接管；只有复杂任务需要压缩上下文时，模型才负责关闭当前 Runtime Phase。
			"build":    {"list_files", "read_file", "search_text", "write_file", "run_command", "done_phase"},
			"readonly": {"list_files", "read_file", "search_text"},
			"none":     {},
		},
		contextPolicies: map[string]ContextPolicy{
			"planning": {ID: "planning", FullState: true, IncludeObjective: true},
			"build":    {ID: "build", FullState: true, IncludeObjective: true},
			"isolated": {ID: "isolated", FullState: false, IncludeObjective: true},
			"approval": {ID: "approval", FullState: false, IncludeObjective: false},
		},
		permissionPolicies: map[string]PermissionPolicy{
			"readonly": {ID: "readonly", AskUser: map[string]string{}, DenyTools: map[string]string{}},
			"build": {ID: "build", AskUser: map[string]string{
				"write_file":  "会创建或覆盖工作区文件",
				"run_command": "执行本地 shell 命令，可能修改文件或调用外部程序",
			}, DenyTools: map[string]string{}},
		},
		transitions: map[string]TransitionPolicy{
			"plan_to_build": {
				ID: "plan_to_build", Command: "start_build", TargetProfile: "build",
				RequiredArtifactType: "plan", RequiredArtifactName: "active",
				CheckpointKind: "plan_to_build", Reason: "plan_to_build",
			},
		},
	}
	r.Register(AgentProfile{
		ID: "plan", DisplayName: "Plan", ToolPolicy: "planning", ContextPolicy: "planning", PermissionPolicy: "readonly", TransitionPolicy: "plan_to_build", IdlePolicy: "wait_user",
		Prompt: `你是 Wisp Plan Agent。目标是用最少调查得到足够可靠的可执行计划。
侦察门槛：Runtime 会按任务复杂度裁剪工具。若任务是独立新增文件、无需理解现有实现即可正确完成，禁止为了“熟悉项目”而调查工作区。若 AGENTS.md 已由 Runtime 注入，不要再次 read_file AGENTS.md。
澄清门槛：只有会显著改变核心结果、成本、破坏性、安全性或不可逆性的歧义才阻塞用户；低风险、易修改细节采用合理默认值并写进计划。
把计划保存为 Plan Artifact。new_plan/edit_plan 同时给出 complexity；只有 complex 任务才给出 phases。保存成功后不要再做例行调查，向用户用其语言简洁说明关键默认值并等待修改或点击开始执行。`,
	})
	r.Register(AgentProfile{
		ID: "build", DisplayName: "Build", ToolPolicy: "build", ContextPolicy: "build", PermissionPolicy: "build", IdlePolicy: "complete_turn",
		Prompt: `你是 Wisp Build Agent。Current Objective、用户后续决定和 Active Plan 是当前执行契约；旧历史不得覆盖它们。用工具产生真实副作用，不要把“我会修改”当成已经修改。
Phase 由 Runtime 从 complex Plan 初始化，你不创建/更新 Phase；仅在当前 complex Phase 实际完成后调用 done_phase。trivial/standard 任务直接执行，不为流程完整性制造 Phase。
可批量提出互不依赖的只读工具调用；有副作用或存在依赖的操作按顺序执行。不要在每个工具前发送“现在开始/接下来”等例行旁白。任务真正完成后用用户语言给出简洁结果、验证和重要路径；Runtime 会保存 Final Summary。`,
	})
	r.Register(AgentProfile{
		ID: "explore", DisplayName: "Explore", ToolPolicy: "readonly", ContextPolicy: "isolated", PermissionPolicy: "readonly", IdlePolicy: "return_result",
		Prompt: `你是只读 Explorer SubAgent。只调查父 Agent 明确提出的问题，不做“顺便看看”的全仓巡游。优先 search_text/定向 read_file；不要读取 .git、构建产物、依赖目录或已注入的 AGENTS.md。最多使用少量高信息量动作，足够回答后立即返回结构化发现：关键文件/位置、证据、风险和父 Agent 可直接采用的结论。`,
	})
	r.Register(AgentProfile{
		ID: "explain", DisplayName: "Explain", ToolPolicy: "none", ContextPolicy: "approval", PermissionPolicy: "readonly", IdlePolicy: "return_result",
		Prompt: `你是 Approval Explainer。只解释当前被冻结的敏感 ToolCall：为什么需要、具体会做什么、风险、可替代方案。你无权执行该敏感工具，不要扩大任务范围。`,
	})
	return r
}

// Register 是 Agent 扩展的稳定入口；Runtime 调度不需要知道具体 profile id。
func (r *ProfileRegistry) Register(profile AgentProfile) {
	if profile.ToolPolicy == "" {
		profile.ToolPolicy = "none"
	}
	if len(profile.Tools) == 0 {
		profile.Tools = append([]string(nil), r.toolPolicies[profile.ToolPolicy]...)
	}
	if profile.ContextPolicy == "" {
		profile.ContextPolicy = "planning"
	}
	if profile.PermissionPolicy == "" {
		profile.PermissionPolicy = "readonly"
	}
	if profile.IdlePolicy == "" {
		profile.IdlePolicy = "wait_user"
	}
	r.profiles[profile.ID] = profile
}

func (r *ProfileRegistry) RegisterToolPolicy(id string, tools []string) {
	r.toolPolicies[id] = append([]string(nil), tools...)
}
func (r *ProfileRegistry) RegisterContextPolicy(policy ContextPolicy) {
	r.contextPolicies[policy.ID] = policy
}
func (r *ProfileRegistry) RegisterPermissionPolicy(policy PermissionPolicy) {
	r.permissionPolicies[policy.ID] = policy
}
func (r *ProfileRegistry) RegisterTransitionPolicy(policy TransitionPolicy) {
	r.transitions[policy.ID] = policy
}

func (r *ProfileRegistry) Get(id string) (AgentProfile, bool) { p, ok := r.profiles[id]; return p, ok }
func (r *ProfileRegistry) List() []AgentProfile {
	out := make([]AgentProfile, 0, len(r.profiles))
	for _, p := range r.profiles {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (r *ProfileRegistry) ContextPolicy(id string) (ContextPolicy, bool) {
	p, ok := r.contextPolicies[id]
	return p, ok
}
func (r *ProfileRegistry) PermissionPolicy(id string) (PermissionPolicy, bool) {
	p, ok := r.permissionPolicies[id]
	return p, ok
}
func (r *ProfileRegistry) Transition(id string) (TransitionPolicy, bool) {
	p, ok := r.transitions[id]
	return p, ok
}
func (r *ProfileRegistry) ArtifactFrozenByProfile(profileID, artifactType, artifactName string) bool {
	for _, transition := range r.transitions {
		if transition.TargetProfile == profileID && transition.RequiredArtifactType == artifactType && transition.RequiredArtifactName == artifactName {
			return true
		}
	}
	return false
}
