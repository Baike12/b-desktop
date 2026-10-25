// Package permission 定义权限模式、规则与审批请求的共享类型。
// 本文件只冻结跨包契约；判定引擎与审批通道由后续文件补齐。
package permission

import (
	"encoding/json"
	"errors"
)

// Mode 是桌面端展示的五档权限模式。
type Mode string

// 权限模式常量。
const (
	ModeDefault           Mode = "default"           // ModeDefault 逐次询问。
	ModeAcceptEdits       Mode = "acceptEdits"       // ModeAcceptEdits 自动接受工作目录内的文件编辑。
	ModePlan              Mode = "plan"              // ModePlan 禁止一切副作用。
	ModeBypassPermissions Mode = "bypassPermissions" // ModeBypassPermissions 放行全部调用（硬拒绝除外）。
	ModeDontAsk           Mode = "dontAsk"           // ModeDontAsk 把任何 ask 收口为 deny。
)

// Effect 是一次权限判定的结果。ask 必须交由调用方阻塞等待用户应答，不得默认放行。
type Effect string

// 判定结果常量。
const (
	EffectAllow Effect = "allow" // EffectAllow 表示直接执行。
	EffectAsk   Effect = "ask"   // EffectAsk 表示必须等待用户审批。
	EffectDeny  Effect = "deny"  // EffectDeny 表示拒绝执行。
)

// Source 是规则来源，用于展示与诊断；它不参与判定优先级。
type Source string

// 规则来源常量。
const (
	SourceBuiltin Source = "builtin" // SourceBuiltin 是内置安全规则。
	SourceUser    Source = "user"    // SourceUser 是用户全局设置。
	SourceProject Source = "project" // SourceProject 是项目设置。
	SourceSession Source = "session" // SourceSession 是本次会话的临时授权。
)

// MatchMode 决定 Pattern 如何与输入比较。规则必须显式指定 MatchMode：
// 判定引擎不认识工具名，无法替规则猜默认模式，空值视为非法规则。
//   - MatchExact：整串相等；Bash 的 always 规则用它，复合命令也安全。
//   - MatchPrefix：前缀匹配；仅当命令不含 shell 元字符时生效。
//   - MatchGlob：路径 glob，委托 tool.MatchPath。
//   - MatchJSON：输入 JSON 的子串匹配，仅用于自建规则。
type MatchMode string

// 匹配模式常量。
const (
	MatchExact  MatchMode = "exact"  // MatchExact 表示整串相等。
	MatchPrefix MatchMode = "prefix" // MatchPrefix 表示前缀匹配；命令含 shell 元字符时不生效。
	MatchGlob   MatchMode = "glob"   // MatchGlob 表示路径 glob，委托 tool.MatchPath。
	MatchJSON   MatchMode = "json"   // MatchJSON 表示输入 JSON 的子串匹配。
)

// Rule 是一条权限规则。Tool 支持 "*"；Pattern 为空表示该工具的所有调用
// （MatchGlob 除外：glob 规则的空 Pattern 非法）。
type Rule struct {
	ID        string    `json:"id"`
	Tool      string    `json:"tool"`
	Pattern   string    `json:"pattern,omitempty"`
	MatchMode MatchMode `json:"matchMode"`
	Effect    Effect    `json:"effect"`
	Source    Source    `json:"source"`
	Note      string    `json:"note,omitempty"`
}

// Request 是一次权限判定输入。
// Paths、WriteFiles 与 NoApproval 由 agent 通过 tool.PathAware 与 tool.AlwaysAllow 填充；
// 判定引擎不解析工具参数里的路径字段，也不认识具体工具名。
type Request struct {
	SessionID  string
	ToolName   string
	Input      json.RawMessage
	ReadOnly   bool
	WriteFiles bool     // 本次调用会写文件（ReadOnly=false 且 Paths 非空）
	NoApproval bool     // 工具声明无副作用且策略上默认放行
	Paths      []string // 绝对路径，已解析符号链接
	WorkDir    string
}

// Decision 是判定输出。allow 与 deny 可直接执行；ask 需要调用方走审批通道。
type Decision struct {
	Effect         Effect `json:"effect"`
	Reason         string `json:"reason"`
	RuleID         string `json:"ruleId,omitempty"`
	MatchedPattern string `json:"matchedPattern,omitempty"`
}

// ApprovalOption 是展示给用户的一个审批选项。
type ApprovalOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ApprovalView 是一次待审批请求的完整快照。
// 它同时用于事件推送与「重新枚举待审批」，必须包含前端弹窗所需的全部字段。
type ApprovalView struct {
	RequestID string           `json:"requestId"`
	SessionID string           `json:"sessionId"`
	ToolName  string           `json:"toolName"`
	Input     json.RawMessage  `json:"input"`
	Reason    string           `json:"reason"`
	Options   []ApprovalOption `json:"options"`
}

// Response 是前端对一次审批请求的答复。
// agent 根据 Option 自行推导要写回的规则，因此这里没有规则字段。
type Response struct {
	// Option 取值：allow_once、allow_always、deny_once、deny_always。
	Option string `json:"option"`
	// UpdatedInput 是前端改写后的工具输入（JSON 对象）。
	// 交互类工具（AskUserQuestion）用它在放行前把用户应答回填进输入；
	// 为空表示保持原输入。仅 allow 类选项会使用它。
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
}

// 审批选项常量。
const (
	OptionAllowOnce   = "allow_once"   // OptionAllowOnce 表示仅放行本次调用。
	OptionAllowAlways = "allow_always" // OptionAllowAlways 表示放行并写入会话规则。
	OptionDenyOnce    = "deny_once"    // OptionDenyOnce 表示仅拒绝本次调用。
	OptionDenyAlways  = "deny_always"  // OptionDenyAlways 表示拒绝并写入会话规则。
)

// ErrRuleInvalid 表示规则非法：MatchMode 为空、MatchGlob 无 Pattern、Effect 未知。
var ErrRuleInvalid = errors.New("permission: invalid rule")
