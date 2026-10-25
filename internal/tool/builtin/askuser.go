package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Baike12/b-desktop/internal/tool"
)

// askUserQuestionSchema 是 AskUserQuestion 的输入 schema。
// 与参考实现对齐：1-4 个问题，每题 2-4 个选项，UI 自动提供 "Other"。
var askUserQuestionSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "questions": {
      "type": "array",
      "minItems": 1,
      "maxItems": 4,
      "items": {
        "type": "object",
        "properties": {
          "question": {"type": "string", "description": "The full question text, ending with a question mark."},
          "header": {"type": "string", "description": "A very short label (max 12 chars) shown as a chip/tag."},
          "options": {
            "type": "array",
            "minItems": 2,
            "maxItems": 4,
            "items": {
              "type": "object",
              "properties": {
                "label": {"type": "string", "description": "The display text of the option (1-5 words)."},
                "description": {"type": "string", "description": "Explanation of what this option means or what happens if chosen."},
                "preview": {"type": "string", "description": "Optional content rendered for side-by-side comparison (mockup, code snippet, config). Only used for single-select questions."}
              },
              "required": ["label", "description"],
              "additionalProperties": false
            }
          },
          "multiSelect": {"type": "boolean", "default": false, "description": "Set true to allow selecting multiple options."}
        },
        "required": ["question", "header", "options", "multiSelect"],
        "additionalProperties": false
      }
    },
    "answers": {
      "type": "object",
      "additionalProperties": {"type": "string"},
      "description": "User answers collected by the permission component. Do not fill this yourself."
    },
    "annotations": {
      "type": "object",
      "additionalProperties": {
        "type": "object",
        "properties": {
          "preview": {"type": "string"},
          "notes": {"type": "string"}
        },
        "additionalProperties": false
      },
      "description": "Optional per-question annotations from the user (selected preview, free-form notes). Keyed by question text."
    }
  },
  "required": ["questions"],
  "additionalProperties": false
}`)

// AskUserQuestion 向用户提出选择题并等待应答。它实现了 tool.Tool 与
// tool.Interactive：agent 对它的调用强制走审批通道，前端把审批对话框
// 渲染成问答界面，用户提交的 answers 经审批通道回填进输入后再执行。
// 它本身只做校验与格式化，不产生任何副作用。
type AskUserQuestion struct{}

// NewAskUserQuestion 创建 AskUserQuestion 工具。
func NewAskUserQuestion() tool.Tool { return &AskUserQuestion{} }

// Name 返回工具名。
func (a *AskUserQuestion) Name() string { return "AskUserQuestion" }

// Description 返回给模型看的说明。
func (a *AskUserQuestion) Description() string {
	return "Use this tool when you are blocked on a decision that is genuinely the user's to make: one you cannot resolve " +
		"from the request, the code, or sensible defaults. Ask 1-4 questions with 2-4 concrete options each; " +
		"the UI always provides an \"Other\" free-text choice, so never add one yourself. " +
		"Use multiSelect=true only when choices are not mutually exclusive. " +
		"Use the optional preview field to let users visually compare concrete artifacts (mockups, code, configs); " +
		"previews are only supported for single-select questions. " +
		"If you recommend a specific option, put it first and add \" (Recommended)\" to its label. " +
		"Do not use this tool for simple confirmations or facts you can verify yourself."
}

// InputSchema 返回 JSON Schema。
func (a *AskUserQuestion) InputSchema() json.RawMessage { return askUserQuestionSchema }

// ReadOnly 报告该调用只读：它不修改任何状态。
func (a *AskUserQuestion) ReadOnly(json.RawMessage) bool { return true }

// ConcurrencySafe 报告该调用可并行；并发安全由审批通道串行化用户交互。
func (a *AskUserQuestion) ConcurrencySafe(json.RawMessage) bool { return true }

// Interactive 报告该工具必须与用户交互，agent 据此强制走审批通道。
func (a *AskUserQuestion) Interactive() bool { return true }

// askQuestion 是单题输入。
type askQuestion struct {
	Question    string      `json:"question"`
	Header      string      `json:"header"`
	Options     []askOption `json:"options"`
	MultiSelect bool        `json:"multiSelect"`
}

// askOption 是单个选项输入。
type askOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Preview     string `json:"preview,omitempty"`
}

// askInput 是 AskUserQuestion 的完整输入。
type askInput struct {
	Questions   []askQuestion     `json:"questions"`
	Answers     map[string]string `json:"answers"`
	Annotations map[string]struct {
		Preview string `json:"preview,omitempty"`
		Notes   string `json:"notes,omitempty"`
	} `json:"annotations"`
}

// Run 校验输入并格式化用户应答。answers 缺失只会在审批通道被绕过时出现
// （正常路径前端必先回填），按错误结果返回，不默认代答。
func (a *AskUserQuestion) Run(ctx context.Context, input json.RawMessage, progress tool.ProgressFunc) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, fmt.Errorf("AskUserQuestion: %w", err)
	}
	var in askInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{IsError: true, Content: "invalid input: " + err.Error()}, nil
	}
	if err := validateAskInput(&in); err != nil {
		return tool.Result{IsError: true, Content: err.Error()}, nil
	}
	if len(in.Answers) == 0 {
		return tool.Result{
			IsError: true,
			Content: "The user did not provide answers to these questions. Continue using your best judgment; " +
				"do not re-ask the same questions in a loop.",
		}, nil
	}

	questions := make(map[string]struct{}, len(in.Questions))
	var parts []string
	for _, q := range in.Questions {
		if _, ok := in.Answers[q.Question]; !ok {
			continue
		}
		questions[q.Question] = struct{}{}
		parts = append(parts, fmt.Sprintf("%q=%q", q.Question, in.Answers[q.Question]))
		if ann, ok := in.Annotations[q.Question]; ok {
			if strings.TrimSpace(ann.Preview) != "" {
				parts = append(parts, "selected preview:\n"+ann.Preview)
			}
			if strings.TrimSpace(ann.Notes) != "" {
				parts = append(parts, "user notes: "+ann.Notes)
			}
		}
	}

	var b strings.Builder
	if len(parts) < len(in.Questions) {
		fmt.Fprintf(&b,
			"The user answered %d of %d questions. Provided answers: %s. "+
				"Proceed with the answered questions and your best judgment for the rest.",
			len(parts), len(in.Questions), strings.Join(parts, ", "))
	} else {
		fmt.Fprintf(&b,
			"User has answered your questions: %s. You can now continue with the user's answers in mind.",
			strings.Join(parts, ", "))
	}
	content := b.String()
	return tool.Result{Content: content, Display: content}, nil
}

// validateAskInput 校验题量、选项数与唯一性；错误文本直接回灌给模型。
func validateAskInput(in *askInput) error {
	if len(in.Questions) == 0 {
		return fmt.Errorf("invalid input: questions is required (1-4 questions)")
	}
	if len(in.Questions) > 4 {
		return fmt.Errorf("invalid input: at most 4 questions per call")
	}
	seenQuestions := make(map[string]struct{}, len(in.Questions))
	for _, q := range in.Questions {
		if strings.TrimSpace(q.Question) == "" {
			return fmt.Errorf("invalid input: every question needs non-empty text")
		}
		if strings.TrimSpace(q.Header) == "" {
			return fmt.Errorf("invalid input: question %q needs a short header (max 12 chars)", q.Question)
		}
		if len([]rune(q.Header)) > 12 {
			return fmt.Errorf("invalid input: header %q exceeds 12 characters", q.Header)
		}
		if _, dup := seenQuestions[q.Question]; dup {
			return fmt.Errorf("invalid input: question texts must be unique: %q", q.Question)
		}
		seenQuestions[q.Question] = struct{}{}
		if len(q.Options) < 2 || len(q.Options) > 4 {
			return fmt.Errorf("invalid input: question %q needs 2-4 options", q.Question)
		}
		seen := make(map[string]struct{}, len(q.Options))
		for _, opt := range q.Options {
			if strings.TrimSpace(opt.Label) == "" {
				return fmt.Errorf("invalid input: question %q has an option with empty label", q.Question)
			}
			if _, dup := seen[opt.Label]; dup {
				return fmt.Errorf("invalid input: option labels must be unique within question %q: %q", q.Question, opt.Label)
			}
			seen[opt.Label] = struct{}{}
		}
	}
	// answers 里的键必须能对应到某个问题，防止模型伪造作答。
	keys := make([]string, 0, len(in.Answers))
	for k := range in.Answers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := seenQuestions[k]; !ok {
			return fmt.Errorf("invalid input: answer refers to unknown question %q", k)
		}
	}
	return nil
}
