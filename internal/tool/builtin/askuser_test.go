package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Baike12/b-desktop/internal/tool"
)

// TestAskUserQuestionContract 验证契约方法。
func TestAskUserQuestionContract(t *testing.T) {
	tl := NewAskUserQuestion()
	if tl.Name() != "AskUserQuestion" {
		t.Fatalf("name = %q", tl.Name())
	}
	if !tl.ReadOnly(nil) || !tl.ConcurrencySafe(nil) {
		t.Fatal("AskUserQuestion must be read-only and concurrency safe")
	}
	iv, ok := tl.(tool.Interactive)
	if !ok || !iv.Interactive() {
		t.Fatal("AskUserQuestion must implement tool.Interactive = true")
	}
}

// TestAskUserQuestionValidates 验证输入校验。
func TestAskUserQuestionValidates(t *testing.T) {
	tl := NewAskUserQuestion()
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no questions",
			input: `{}`,
			want:  "questions is required",
		},
		{
			name:  "single option",
			input: `{"questions":[{"question":"Q?","header":"H","options":[{"label":"A","description":"d"}],"multiSelect":false}]}`,
			want:  "2-4 options",
		},
		{
			name: "duplicate questions",
			input: `{"questions":[
				{"question":"Q?","header":"H1","options":[{"label":"A","description":"d"},{"label":"B","description":"d"}],"multiSelect":false},
				{"question":"Q?","header":"H2","options":[{"label":"A","description":"d"},{"label":"B","description":"d"}],"multiSelect":false}]}`,
			want: "unique",
		},
		{
			name:  "duplicate labels",
			input: `{"questions":[{"question":"Q?","header":"H","options":[{"label":"A","description":"d"},{"label":"A","description":"d2"}],"multiSelect":false}]}`,
			want:  "unique",
		},
		{
			name:  "header too long",
			input: `{"questions":[{"question":"Q?","header":"0123456789012","options":[{"label":"A","description":"d"},{"label":"B","description":"d"}],"multiSelect":false}]}`,
			want:  "12 characters",
		},
		{
			name:  "answer for unknown question",
			input: `{"questions":[{"question":"Q?","header":"H","options":[{"label":"A","description":"d"},{"label":"B","description":"d"}],"multiSelect":false}],"answers":{"Other":"x"}}`,
			want:  "unknown question",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tl.Run(context.Background(), json.RawMessage(tc.input), nil)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !res.IsError || !strings.Contains(res.Content, tc.want) {
				t.Fatalf("result = %+v, want error containing %q", res, tc.want)
			}
		})
	}
}

// TestAskUserQuestionFormatsAnswers 验证应答格式化。
func TestAskUserQuestionFormatsAnswers(t *testing.T) {
	tl := NewAskUserQuestion()
	input := `{
		"questions":[
			{"question":"选哪个库?","header":"选型","options":[{"label":"A","description":"d"},{"label":"B","description":"d"}],"multiSelect":false},
			{"question":"要不要删?","header":"清理","options":[{"label":"删","description":"d"},{"label":"留","description":"d"}],"multiSelect":false}
		],
		"answers":{"选哪个库?":"B", "要不要删?":"删, 留"},
		"annotations":{"选哪个库?":{"preview":"const x = 1","notes":"用户备注"}}
	}`
	res, err := tl.Run(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	for _, want := range []string{
		`"选哪个库?"="B"`,
		"selected preview:\nconst x = 1",
		"user notes: 用户备注",
		`"要不要删?"="删, 留"`,
		"continue with the user's answers",
	} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("content %q missing %q", res.Content, want)
		}
	}
}

// TestAskUserQuestionPartialAnswers 验证部分应答的回灌文案。
func TestAskUserQuestionPartialAnswers(t *testing.T) {
	tl := NewAskUserQuestion()
	input := `{
		"questions":[
			{"question":"Q1?","header":"H","options":[{"label":"A","description":"d"},{"label":"B","description":"d"}],"multiSelect":false},
			{"question":"Q2?","header":"H","options":[{"label":"A","description":"d"},{"label":"B","description":"d"}],"multiSelect":false}
		],
		"answers":{"Q1?":"A"}
	}`
	res, err := tl.Run(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "answered 1 of 2") || !strings.Contains(res.Content, `"Q1?"="A"`) {
		t.Fatalf("content = %q", res.Content)
	}
}

// TestAskUserQuestionNoAnswers 验证没有应答时返回指引错误。
func TestAskUserQuestionNoAnswers(t *testing.T) {
	tl := NewAskUserQuestion()
	input := `{"questions":[{"question":"Q?","header":"H","options":[{"label":"A","description":"d"},{"label":"B","description":"d"}],"multiSelect":false}]}`
	res, err := tl.Run(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "did not provide answers") {
		t.Fatalf("result = %+v", res)
	}
}
