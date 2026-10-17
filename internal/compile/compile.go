package compile

import (
	"context"
	"encoding/json"
	"fmt"

	"wisp/internal/eventlog"
	"wisp/internal/ids"
)

// 常量：占位与链接语义（语义详见包注释）。
const (
	// hiddenPlaceholder 是 hide 的非工具条目在输出中的占位正文。
	hiddenPlaceholder = "[已隐藏]"

	// omittedResult 是孤儿 tool.called 的占位结果正文。
	omittedResult = "omitted"
)

// Role 是消息角色。
type Role string

// 消息角色取值。
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ToolCall 是一次工具调用（assistant 消息携带）。
type ToolCall struct {
	ID         ids.ToolCallID // 内部 ToolCallID（主键）
	ProviderID string         // provider 侧的 tool_use_id，来自 tool.called.attrs.provider_id，可能为空
	Name       string         // 工具名
	Arguments  string         // 调用参数 JSON 原文
}

// Message 是编译输出的一条消息。
// ToolCall 与 ToolResultFor 互斥：assistant 消息带 ToolCall，
// user 消息带 ToolResultFor（工具结果对应的内部调用 ID）。
type Message struct {
	Role          Role           // 消息角色
	Content       string         // 正文（工具调用消息为空，占位消息为 "[已隐藏]"）
	ToolCall      *ToolCall      // assistant：工具调用
	ToolResultFor ids.ToolCallID // user：工具结果对应的内部调用 ID
}

// ManifestItem 是 RequestManifest 的一个条目：回放用的内容定位信息。
// 与 Messages 严格一一对应；占位消息 Placeholder 为 true 且 BlobHash 为空。
type ManifestItem struct {
	ID          string `json:"id"`                    // 内容条目 ID（事件的 subject_id）
	BlobHash    string `json:"blob_hash"`             // 正文在 blobs 表的哈希引用
	Revision    int64  `json:"revision"`              // 采用的事件版本 seq
	Placeholder bool   `json:"placeholder,omitempty"` // 占位消息（无 blob 正文）
}

// Result 是编译结果。
type Result struct {
	Messages     []Message                 // 最终消息列表
	Warnings     []string                  // 非致命问题（如孤儿 called 补占位结果）
	ManifestHash string                    // RequestManifest 的 blob 哈希，供 llm.requested 引用
	ProviderMap  map[ids.ToolCallID]string // 内部 ToolCallID → provider tool_use_id

	manifest []ManifestItem // RequestManifest 原文（render 阶段收集）
}

// entry 是编译过程中的一个内容条目。
type entry struct {
	subject string         // 条目主键（内容事件的 SubjectID）
	ev      eventlog.Event // 采用的内容事件版本
	anchor  string         // insert 分组锚点（base/append 条目为空）
	okey    string         // anchor 组内分数索引（空 = 排最后）
	hidden  bool           // 是否被 hide
	pinSeq  int64          // pin 操作的事件 seq，0 = 未置顶
}

// key 返回条目的去重键：(事件类型, subject)。
func (e *entry) key() string { return e.ev.Type + "|" + e.subject }

// Compile 把指定上下文编译为发给 LLM 的消息列表，算法步骤见包注释。
// 编译成功后会把 RequestManifest 序列化存入 blobs 表并返回其哈希。
func Compile(ctx context.Context, store *eventlog.Store, ctxID ids.ContextID) (*Result, error) {
	// 1. ancestry：沿 base_ctx 链取 root→leaf 的上下文。
	lineage, err := ancestry(ctx, store, ctxID)
	if err != nil {
		return nil, err
	}

	// 2. 基线内容：从叶子上下文的 fork_at 沿 parent_id 回溯收集内容事件。
	list, err := loadBase(ctx, store, lineage[len(lineage)-1].ForkAt)
	if err != nil {
		return nil, err
	}

	// 3. 逐层应用 ctx_ops（root→leaf，每层按 seq 升序）。
	for _, c := range lineage {
		ops, err := store.ListCtxOps(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("compile: 读取上下文 %s 的 ctx_ops 失败: %w", c.ID, err)
		}
		for _, op := range ops {
			if err := applyOp(ctx, store, list, op); err != nil {
				return nil, err
			}
		}
	}

	// 4. 批量读取正文 blob，并读取 result_of 链接建立工具调用分组。
	blobs, err := loadBlobs(ctx, store, list)
	if err != nil {
		return nil, err
	}
	bundles, err := loadBundles(ctx, store, list)
	if err != nil {
		return nil, err
	}
	warnings := enforceBundles(list, bundles)

	// 置顶分区：pinned 按 pinSeq 升序在前，其余保持相对顺序在后。
	list.pinPartition()

	// 6. render：渲染为消息列表，同时生成 RequestManifest。
	res := &Result{ProviderMap: map[ids.ToolCallID]string{}}
	if err := render(list, bundles, blobs, res); err != nil {
		return nil, err
	}

	// 5. validate：不变量校验，失败即返回 error。
	if err := validate(res.Messages, res.ProviderMap); err != nil {
		return nil, err
	}

	// RequestManifest 序列化存 blob，供 llm.requested 引用。
	raw, err := json.Marshal(res.manifest)
	if err != nil {
		return nil, fmt.Errorf("compile: 序列化 RequestManifest 失败: %w", err)
	}
	hash, err := store.PutBlob(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("compile: 写入 RequestManifest blob 失败: %w", err)
	}
	res.ManifestHash = hash
	res.Warnings = warnings
	return res, nil
}

// entryList 是保序的条目列表，维护去重索引。
type entryList struct {
	items []*entry
	index map[string]*entry // key() → 条目
}

func newEntryList() *entryList {
	return &entryList{index: map[string]*entry{}}
}

func (l *entryList) append(e *entry) {
	l.items = append(l.items, e)
	l.index[e.key()] = e
}

// remove 从列表中删除条目（enforceBundles 丢弃孤儿 returned 用）。
func (l *entryList) remove(target *entry) {
	for i, e := range l.items {
		if e == target {
			l.items = append(l.items[:i], l.items[i+1:]...)
			break
		}
	}
	delete(l.index, target.key())
}

// findBySubject 返回 subject 命中的全部条目（tool 调用的 call/result
// 共享 subject，会命中两条），未命中返回 nil。
func (l *entryList) findBySubject(subject string) []*entry {
	var out []*entry
	for _, e := range l.items {
		if e.subject == subject {
			out = append(out, e)
		}
	}
	return out
}

// ancestry 沿 base_ctx 链取 root→leaf 的上下文序列。
func ancestry(ctx context.Context, store *eventlog.Store, ctxID ids.ContextID) ([]eventlog.ContextRow, error) {
	var lineage []eventlog.ContextRow
	seen := map[ids.ContextID]bool{}
	cur := ctxID
	for cur != "" {
		if seen[cur] {
			return nil, fmt.Errorf("compile: 上下文 base_ctx 链存在环: %s", cur)
		}
		seen[cur] = true
		row, err := store.GetContext(ctx, cur)
		if err != nil {
			return nil, fmt.Errorf("compile: 读取上下文 %s 失败: %w", cur, err)
		}
		lineage = append([]eventlog.ContextRow{row}, lineage...) // 前插，得到 root→leaf
		cur = row.BaseCtx
	}
	return lineage, nil
}

// loadBase 从 forkAt 事件沿 parent_id 回溯到根，收集内容事件：
// 按 (事件类型, subject_id) 去重，同 subject 多版本取 seq 最大者，
// 位置取最新版本在链上的时间位置（回溯序反转为时间序）。
// 整条链由 Store.LoadChain 一次查询取出，避免逐事件 N 次查询。
func loadBase(ctx context.Context, store *eventlog.Store, forkAt ids.EventID) (*entryList, error) {
	list := newEntryList()
	// LoadChain 顺序为 forkAt→根（新→旧）；order 记录去重键首次出现的位置。
	var order []string
	byKey := map[string]*entry{}

	evs, err := store.LoadChain(ctx, forkAt)
	if err != nil {
		return nil, fmt.Errorf("compile: 读取 parent_id 链失败: %w", err)
	}
	for _, ev := range evs {
		if !eventlog.IsContentType(ev.Type) {
			continue
		}
		k := ev.Type + "|" + ev.SubjectID
		if old, ok := byKey[k]; ok {
			// 同 subject 多版本：取 seq 最大者，保留最早出现的位置。
			if ev.Seq > old.ev.Seq {
				old.ev = ev
			}
			continue
		}
		e := &entry{subject: ev.SubjectID, ev: ev}
		byKey[k] = e
		order = append(order, k)
	}

	// 反转为时间序（根→forkAt）。
	for i := len(order) - 1; i >= 0; i-- {
		list.append(byKey[order[i]])
	}
	return list, nil
}

// getContent 读取并校验 ctx_op 引用的内容事件。
func getContent(ctx context.Context, store *eventlog.Store, id ids.EventID) (eventlog.Event, error) {
	ev, err := store.Get(ctx, id)
	if err != nil {
		return eventlog.Event{}, fmt.Errorf("compile: 读取 ctx_op 引用的内容事件 %s 失败: %w", id, err)
	}
	if !eventlog.IsContentType(ev.Type) {
		return eventlog.Event{}, fmt.Errorf("compile: ctx_op 引用的事件 %s 类型 %q 不是内容事件", id, ev.Type)
	}
	return ev, nil
}

// applyOp 应用一条 ctx_op。
func applyOp(ctx context.Context, store *eventlog.Store, list *entryList, op eventlog.CtxOpRow) error {
	switch op.Op {
	case eventlog.CtxOpAppend:
		ev, err := getContent(ctx, store, op.NewID)
		if err != nil {
			return err
		}
		e := &entry{subject: ev.SubjectID, ev: ev}
		if _, dup := list.index[e.key()]; dup {
			return fmt.Errorf("compile: append 的条目 %s 已存在", e.key())
		}
		list.append(e)

	case eventlog.CtxOpInsert:
		if op.Anchor == "" {
			return fmt.Errorf("compile: insert 操作 %s 缺少 anchor", op.ID)
		}
		ev, err := getContent(ctx, store, op.NewID)
		if err != nil {
			return err
		}
		e := &entry{subject: ev.SubjectID, ev: ev, anchor: op.Anchor, okey: op.OKey}
		if _, dup := list.index[e.key()]; dup {
			return fmt.Errorf("compile: insert 的条目 %s 已存在", e.key())
		}
		if err := insertInto(list, e); err != nil {
			return err
		}

	case eventlog.CtxOpHide:
		hits := list.findBySubject(op.Target)
		if len(hits) == 0 {
			return fmt.Errorf("compile: hide 目标 %s 不存在", op.Target)
		}
		// 幂等：已隐藏则不动（重复 hide 不产生额外效果）
		for _, e := range hits {
			e.hidden = true
		}

	case eventlog.CtxOpReplace:
		ev, err := getContent(ctx, store, op.NewID)
		if err != nil {
			return err
		}
		var target *entry
		for _, e := range list.findBySubject(op.Target) {
			if e.ev.Type == ev.Type {
				target = e
				break
			}
		}
		if target == nil {
			return fmt.Errorf("compile: replace 目标 %s（类型 %s）不存在", op.Target, ev.Type)
		}
		// 原 target 不得再以任何形式出现
		delete(list.index, target.key())
		target.subject = ev.SubjectID
		target.ev = ev
		if _, dup := list.index[target.key()]; dup {
			return fmt.Errorf("compile: replace 后的条目 %s 已存在", target.key())
		}
		list.index[target.key()] = target

	case eventlog.CtxOpPin:
		hits := list.findBySubject(op.Target)
		if len(hits) == 0 {
			return fmt.Errorf("compile: pin 目标 %s 不存在", op.Target)
		}
		// 幂等：已置顶则保留最早的 pin 序
		for _, e := range hits {
			if e.pinSeq == 0 {
				e.pinSeq = op.Seq
			}
		}

	default:
		return fmt.Errorf("compile: 未知的 ctx.op 操作 %q", op.Op)
	}
	return nil
}

// okeyLess 比较分数索引：空串排最后，其余按字典序。
func okeyLess(a, b string) bool {
	if a == "" {
		return false
	}
	if b == "" {
		return true
	}
	return a < b
}

// insertInto 把条目插入 anchor 组：组紧随 anchor 条目（同 subject 多条目
// 时取最前者，通常即 tool.called），组内按 okey 排序（相等时后提交者排后）。
// anchor 条目不存在返回 error。
func insertInto(list *entryList, e *entry) error {
	items := list.items
	// 定位 anchor 条目（首次匹配）
	anchorIdx := -1
	for i, it := range items {
		if it.subject == e.anchor {
			anchorIdx = i
			break
		}
	}
	if anchorIdx < 0 {
		return fmt.Errorf("compile: insert 锚点 %s 不存在", e.anchor)
	}
	// 组内成员（anchor 相同的条目）在列表中可能不连续（例如结果条目锚定
	// 组内某个调用），按列表顺序收集成员，在组内按 okey 找插入名次：
	// 插在第一个 okey 大于新条目的成员之前；否则插在最后一个成员之后。
	var members []int
	for i, it := range items {
		if it.anchor == e.anchor {
			members = append(members, i)
		}
	}
	rank := len(members)
	for k, mi := range members {
		if okeyLess(e.okey, items[mi].okey) {
			rank = k
			break
		}
	}
	j := anchorIdx + 1
	if rank < len(members) {
		j = members[rank]
	} else if len(members) > 0 {
		j = members[len(members)-1] + 1
	}
	items = append(items, nil)
	copy(items[j+1:], items[j:])
	items[j] = e
	list.items = items
	list.index[e.key()] = e
	return nil
}

// pinPartition 置顶分区：pinned 条目按 pinSeq 升序排在前面，
// 未置顶条目保持相对顺序排在后面。稳定排序保证同 pinSeq 的
// tool 调用组内顺序不变。
func (l *entryList) pinPartition() {
	items := l.items
	pinned := make([]*entry, 0, len(items))
	var rest []*entry
	for _, e := range items {
		if e.pinSeq > 0 {
			pinned = append(pinned, e)
		} else {
			rest = append(rest, e)
		}
	}
	// 简单插入排序：pinned 规模很小
	for i := 1; i < len(pinned); i++ {
		for k := i; k > 0 && pinned[k-1].pinSeq > pinned[k].pinSeq; k-- {
			pinned[k-1], pinned[k] = pinned[k], pinned[k-1]
		}
	}
	l.items = append(pinned, rest...)
}

// loadBlobs 批量读取条目引用的正文 blob，避免 render 时逐条查询。
func loadBlobs(ctx context.Context, store *eventlog.Store, list *entryList) (map[string][]byte, error) {
	hashes := make([]string, 0, len(list.items))
	for _, e := range list.items {
		if e.ev.Blob != "" {
			hashes = append(hashes, e.ev.Blob)
		}
	}
	blobs, err := store.GetBlobs(ctx, hashes)
	if err != nil {
		return nil, fmt.Errorf("compile: 读取正文 blob 失败: %w", err)
	}
	return blobs, nil
}

// bundles 是 result_of 关系的预计算索引：按事件 ID 找条目，按链接找伙伴。
// enforceBundles 与 render 共用一次查询结果，避免重复读 links 表。
type bundles struct {
	byEventID        map[string]*entry
	partnerOf        map[*entry]*entry
	returnedToCalled map[string][]string
}

// loadBundles 读取 result_of 链接并建立分组索引。
func loadBundles(ctx context.Context, store *eventlog.Store, list *entryList) (*bundles, error) {
	links, err := store.ListLinks(ctx, "", "", eventlog.RelResultOf)
	if err != nil {
		return nil, fmt.Errorf("compile: 读取 result_of 链接失败: %w", err)
	}
	b := &bundles{
		byEventID:        make(map[string]*entry, len(list.items)),
		partnerOf:        map[*entry]*entry{},
		returnedToCalled: map[string][]string{},
	}
	for _, e := range list.items {
		b.byEventID[e.ev.ID.String()] = e
	}
	for _, l := range links {
		b.returnedToCalled[l.Src] = append(b.returnedToCalled[l.Src], l.Dst)
	}
	for rid, cids := range b.returnedToCalled {
		r := b.byEventID[rid]
		if r == nil {
			continue
		}
		for _, cid := range cids {
			if c := b.byEventID[cid]; c != nil {
				b.partnerOf[r] = c
				b.partnerOf[c] = r
			}
		}
	}
	return b, nil
}

// enforceBundles 让 tool.called 与 tool.returned 原子成组：
// 组内任一被 hide 则整组隐藏；孤儿 returned 丢弃；孤儿 called 记 warning。
func enforceBundles(list *entryList, b *bundles) []string {
	var warnings []string
	var orphanReturned []*entry

	for _, e := range list.items {
		if e.ev.Type != eventlog.TypeToolReturned {
			continue
		}
		claimed := false
		for _, cid := range b.returnedToCalled[e.ev.ID.String()] {
			if b.byEventID[cid] != nil {
				claimed = true
				break
			}
		}
		if !claimed {
			orphanReturned = append(orphanReturned, e)
		}
	}

	claimedCalled := make(map[string]bool)
	for rid, cids := range b.returnedToCalled {
		if b.byEventID[rid] == nil {
			continue
		}
		for _, cid := range cids {
			if b.byEventID[cid] != nil {
				claimedCalled[cid] = true
			}
		}
	}
	for _, e := range list.items {
		if e.ev.Type != eventlog.TypeToolCalled || claimedCalled[e.ev.ID.String()] {
			continue
		}
		warnings = append(warnings,
			fmt.Sprintf("compile: 工具调用 %s 缺少结果，已补占位结果 %q", e.subject, omittedResult))
	}

	// 组内任一被 hide 则整组隐藏。
	for _, e := range list.items {
		if p := b.partnerOf[e]; p != nil && (e.hidden || p.hidden) {
			e.hidden = true
			p.hidden = true
		}
	}

	for _, e := range orphanReturned {
		list.remove(e)
	}
	return warnings
}

// entryRole 返回条目渲染时的角色。
func entryRole(e *entry) (Role, error) {
	switch e.ev.Type {
	case eventlog.TypeToolCalled:
		return RoleAssistant, nil
	case eventlog.TypeToolReturned:
		return RoleUser, nil
	case eventlog.TypeMessageCreated:
		var attrs eventlog.MessageCreatedAttrs
		if err := json.Unmarshal(e.ev.Attrs, &attrs); err != nil {
			return "", fmt.Errorf("compile: 解析 message.created attrs 失败: %w", err)
		}
		switch Role(attrs.Role) {
		case RoleUser, RoleAssistant:
			return Role(attrs.Role), nil
		}
		return "", fmt.Errorf("compile: 未知的消息角色 %q", attrs.Role)
	}
	return "", fmt.Errorf("compile: 事件类型 %q 不是内容事件", e.ev.Type)
}

// blobText 从预加载的 blob 表取事件正文。
func blobText(ev eventlog.Event, blobs map[string][]byte) (string, error) {
	if ev.Blob == "" {
		return "", nil
	}
	data, ok := blobs[ev.Blob]
	if !ok {
		return "", fmt.Errorf("compile: 事件 %s 的 blob %s 不存在", ev.ID, ev.Blob)
	}
	return string(data), nil
}

// render 把条目列表渲染为消息列表，并生成与消息严格一一对应的
// RequestManifest 与 provider 映射。隐藏的非工具条目渲染为同角色占位消息，
// 隐藏的工具组整组丢弃；占位消息的清单条目标记 Placeholder 且 BlobHash 为空。
func render(list *entryList, b *bundles, blobs map[string][]byte, res *Result) error {
	msgs := make([]Message, 0, len(list.items))
	var manifest []ManifestItem
	rendered := map[*entry]bool{}

	for _, e := range list.items {
		if rendered[e] {
			continue
		}
		role, err := entryRole(e)
		if err != nil {
			return err
		}

		// 隐藏条目：工具组整组丢弃（伙伴一同跳过），其余渲染同角色占位消息
		if e.hidden {
			if e.ev.Type == eventlog.TypeToolCalled || e.ev.Type == eventlog.TypeToolReturned {
				if p := b.partnerOf[e]; p != nil {
					rendered[p] = true
				}
				rendered[e] = true
				continue
			}
			rendered[e] = true
			msgs = append(msgs, Message{Role: role, Content: hiddenPlaceholder})
			manifest = append(manifest, ManifestItem{ID: e.subject, Revision: e.ev.Seq, Placeholder: true})
			continue
		}

		switch e.ev.Type {
		case eventlog.TypeMessageCreated:
			text, err := blobText(e.ev, blobs)
			if err != nil {
				return err
			}
			rendered[e] = true
			msgs = append(msgs, Message{Role: role, Content: text})
			manifest = append(manifest, ManifestItem{ID: e.subject, BlobHash: e.ev.Blob, Revision: e.ev.Seq})

		case eventlog.TypeToolCalled:
			var attrs eventlog.ToolCalledAttrs
			if err := json.Unmarshal(e.ev.Attrs, &attrs); err != nil {
				return fmt.Errorf("compile: 解析 tool.called attrs 失败: %w", err)
			}
			args, err := blobText(e.ev, blobs)
			if err != nil {
				return err
			}
			tcID := ids.ToolCallID(e.subject)
			res.ProviderMap[tcID] = attrs.ProviderID
			rendered[e] = true
			msgs = append(msgs, Message{Role: RoleAssistant, ToolCall: &ToolCall{
				ID:         tcID,
				ProviderID: attrs.ProviderID,
				Name:       attrs.Name,
				Arguments:  args,
			}})
			manifest = append(manifest, ManifestItem{ID: e.subject, BlobHash: e.ev.Blob, Revision: e.ev.Seq})

			// 结果：优先取伙伴条目；孤儿 called 补占位结果
			p := b.partnerOf[e]
			if p != nil && !p.hidden {
				text, err := blobText(p.ev, blobs)
				if err != nil {
					return err
				}
				rendered[p] = true
				msgs = append(msgs, Message{Role: RoleUser, Content: text, ToolResultFor: tcID})
				manifest = append(manifest, ManifestItem{ID: p.subject, BlobHash: p.ev.Blob, Revision: p.ev.Seq})
			} else {
				rendered[e] = true
				msgs = append(msgs, Message{Role: RoleUser, Content: omittedResult, ToolResultFor: tcID})
				manifest = append(manifest, ManifestItem{ID: e.subject, Revision: e.ev.Seq, Placeholder: true})
			}

		case eventlog.TypeToolReturned:
			// 未被调用的 returned（其 called 未被渲染）属于内部不一致，报错
			return fmt.Errorf("compile: tool.returned 条目 %s 的调用未被渲染", e.subject)
		}
	}

	res.Messages = msgs
	res.manifest = manifest
	return nil
}

// sameRoleAdjacent 判断同角色相邻是否合法：
//   - assistant：后一条必须是工具调用（正文+工具调用、或多个并行调用
//     视为同一逻辑 assistant 消息）；正文后紧跟正文仍非法。
//   - user：后一条必须是工具结果（一次调用的多条结果相邻合法）；
//     正文后紧跟正文仍非法。
func sameRoleAdjacent(prev, cur Message) bool {
	switch cur.Role {
	case RoleAssistant:
		return cur.ToolCall != nil
	case RoleUser:
		return cur.ToolResultFor != ""
	}
	return false
}

// validate 校验编译不变量：非空、首条非 assistant、assistant 正文消息可
// 紧跟工具调用消息（同一逻辑 assistant 消息）、user 工具结果可相邻、
// 工具调用与结果一一配对、每个工具调用都有非空 provider_id 映射。
// 任何一条不满足都返回 error。
func validate(msgs []Message, providerMap map[ids.ToolCallID]string) error {
	if len(msgs) == 0 {
		return fmt.Errorf("compile: 编译结果为空")
	}
	if msgs[0].Role == RoleAssistant {
		return fmt.Errorf("compile: 首条消息为 assistant，违反不变量")
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == msgs[i-1].Role && !sameRoleAdjacent(msgs[i-1], msgs[i]) {
			return fmt.Errorf("compile: 第 %d、%d 条消息角色均为 %s，违反角色分段规则", i-1, i, msgs[i].Role)
		}
	}
	calls := map[ids.ToolCallID]int{}
	for i, m := range msgs {
		if m.ToolCall != nil {
			if _, dup := calls[m.ToolCall.ID]; dup {
				return fmt.Errorf("compile: 工具调用 ID %s 重复", m.ToolCall.ID)
			}
			if providerMap[m.ToolCall.ID] == "" {
				return fmt.Errorf("compile: 工具调用 %s 缺少 provider_id 映射", m.ToolCall.ID)
			}
			calls[m.ToolCall.ID] = i
		}
	}
	results := map[ids.ToolCallID]int{}
	for i, m := range msgs {
		if m.ToolResultFor != "" {
			if _, dup := results[m.ToolResultFor]; dup {
				return fmt.Errorf("compile: 工具结果 ID %s 重复", m.ToolResultFor)
			}
			results[m.ToolResultFor] = i
		}
	}
	for id, ci := range calls {
		ri, ok := results[id]
		if !ok {
			return fmt.Errorf("compile: 工具调用 %s 缺少配对结果", id)
		}
		if ri <= ci {
			return fmt.Errorf("compile: 工具调用 %s 的结果未出现在调用之后", id)
		}
	}
	for id := range results {
		if _, ok := calls[id]; !ok {
			return fmt.Errorf("compile: 工具结果 %s 没有对应调用", id)
		}
	}
	return nil
}
