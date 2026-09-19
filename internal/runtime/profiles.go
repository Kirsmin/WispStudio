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
			"build":    {"list_files", "read_file", "search_text", "verify_file", "write_file", "run_command"},
			"readonly": {"list_files", "read_file", "search_text", "verify_file"},
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
		Prompt: `你负责把需求转成可执行的 Active Plan，保存一次 new_plan（需求改变才 edit_plan）。
调查门槛：如果可独立新增文件且无需现有代码事实即可实现，直接保存简短 Plan，禁止先列目录、读取 AGENTS.md 或启动 Explorer。已注入的项目指令在 Context 中，无需重复读取。对仓库改造只读与目标有关的文件；Explorer 仅在跨模块依赖确实无法快速定位时使用。
只针对重大成本/不可逆歧义询问；六位纯数字默认 000000–999999、每行一条。计划写清产物、执行、验收及有根据的风险，小任务无需拆 Phase。保存后简洁告诉用户可开始执行，不要再逐项索要确认。`,
	})
	r.Register(AgentProfile{
		ID: "build", DisplayName: "Build", ToolPolicy: "build", ContextPolicy: "build", PermissionPolicy: "build", IdlePolicy: "complete_turn",
		Prompt: `你是 Build Agent。按当前有效目标、最新用户修改和 Active Plan 执行，最新明确指令覆盖冲突的计划文本。不要重新提出用户已解答的问题。
Runtime 自动管理阶段与 Checkpoint，不要为了状态维护调用工具；小任务直接写入→运行→verify_file 验证→结论。verify_file 是无需 Shell 权限的只读文件校验，优先使用；执行程序仍使用 run_command 并遵守审批。
必要时再查代码；独立新增文件不列仓库、也不读 AGENTS.md。工具可批量提交至多四个独立只读调用；依赖工具结果的动作必须等待结果再发起。没有产物或结果时不要声称完成。最终用用户语言简明列出实际完成、验证证据和未完成项。`,
	})
	r.Register(AgentProfile{
		ID: "explore", DisplayName: "Explore", ToolPolicy: "readonly", ContextPolicy: "isolated", PermissionPolicy: "readonly", IdlePolicy: "return_result",
		Prompt: `你是有严格预算的只读 Explorer。只检查与委派问题相关的少数源码文件；已注入的 AGENTS.md 不再读取，勿搜索 .git、dist 或构建产物。最多五轮工具动作，取得足够证据即停止；返回关键位置、证据、风险，不做无目的全仓搜索。`,
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
