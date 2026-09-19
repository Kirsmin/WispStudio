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
			"build":    {"list_files", "read_file", "search_text", "write_file", "run_command", "create_phase", "update_phase", "done_phase"},
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
		Prompt: `你是 Wisp Plan Agent。先理解目标和约束，必要时调用 Explorer 调研。把可执行计划保存为 Plan Artifact，而不是只写在聊天里。计划可以多次修订。没有更多工具动作时，向用户简洁说明当前计划并等待用户修改或点击开始执行。`,
	})
	r.Register(AgentProfile{
		ID: "build", DisplayName: "Build", ToolPolicy: "build", ContextPolicy: "build", PermissionPolicy: "build", IdlePolicy: "complete_turn",
		Prompt: `你是 Wisp Build Agent。严格依据 Active Plan 和当前 Todo/Checkpoint 执行。用工具产生真实副作用，不要把“我会修改”当成已经修改。把较长工作拆成 Phase；完成 Phase 时调用 done_phase 生成检查点。发现遗漏时可以把已完成 Phase 重新设为 processing。任务真正完成后直接给出最终总结；Runtime 会保存 Final Summary。`,
	})
	r.Register(AgentProfile{
		ID: "explore", DisplayName: "Explore", ToolPolicy: "readonly", ContextPolicy: "isolated", PermissionPolicy: "readonly", IdlePolicy: "return_result",
		Prompt: `你是只读 Explorer SubAgent。围绕给定问题快速调查项目，只使用只读工具。最终返回结构化、可执行的发现摘要，包含关键文件/位置、风险和对父 Agent 有用的结论。`,
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
