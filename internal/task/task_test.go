package task

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestRegistry 创建输出目录在临时目录、回调写入 channel 的注册表。
func newTestRegistry(t *testing.T) (*Registry, chan Task) {
	t.Helper()
	dir := t.TempDir()
	notify := make(chan Task, 16)
	reg, err := NewRegistry(Config{
		OutputDir: dir,
		OnComplete: func(task Task) {
			notify <- task
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg, notify
}

// TestStartBashLifecycle 验证后台命令的完整生命周期与输出落盘。
func TestStartBashLifecycle(t *testing.T) {
	reg, notify := newTestRegistry(t)
	handle := reg.StartBash(StartBashInput{
		SessionID:   "s1",
		Description: "echo hi",
		Command:     "echo hello-background",
	})
	if handle.Err() != nil {
		t.Fatalf("start: %v", handle.Err())
	}
	got, ok := reg.WaitTerminal(handle.TaskID(), 10*time.Second)
	if !ok || got.Status != StatusCompleted {
		t.Fatalf("task = %+v", got)
	}
	if got.ExitCode != 0 {
		t.Fatalf("exit code = %d", got.ExitCode)
	}
	if got.SessionID != "s1" {
		t.Fatalf("session = %q", got.SessionID)
	}
	text, truncated, total, err := reg.OutputTail(handle.TaskID(), 1024)
	if err != nil || truncated || total == 0 {
		t.Fatalf("tail = %q truncated=%v total=%d err=%v", text, truncated, total, err)
	}
	if !strings.Contains(text, "hello-background") {
		t.Fatalf("output missing text: %q", text)
	}
	select {
	case done := <-notify:
		if done.ID != handle.TaskID() {
			t.Fatalf("notification for %s, want %s", done.ID, handle.TaskID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no completion notification")
	}
	xml := FormatBashNotification(got)
	for _, want := range []string{"<task-notification>", "<status>completed</status>", "completed (exit code 0)"} {
		if !strings.Contains(xml, want) {
			t.Fatalf("xml %q missing %q", xml, want)
		}
	}
}

// TestStartBashStopSuppressesNotification 验证主动停止抑制通知并写成 stopped。
func TestStartBashStopSuppressesNotification(t *testing.T) {
	reg, notify := newTestRegistry(t)
	handle := reg.StartBash(StartBashInput{Command: "sleep 30"})
	got, err := reg.Stop(handle.TaskID())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got.Status != StatusStopped {
		t.Fatalf("status = %s, want stopped", got.Status)
	}
	select {
	case <-notify:
		t.Fatal("stopped bash task must not notify")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestTaskOutputTool 验证 TaskOutput 的 not_ready / 等待 / 完成 三种返回。
func TestTaskOutputTool(t *testing.T) {
	reg, _ := newTestRegistry(t)
	tl := NewTaskOutput(reg)
	if !tl.ReadOnly(nil) || !tl.ConcurrencySafe(nil) {
		t.Fatal("TaskOutput must be read-only and concurrency safe")
	}

	handle := reg.StartBash(StartBashInput{Command: "echo done-later", Description: "echo"})
	notReady, err := tl.Run(context.Background(), json.RawMessage(`{"task_id":"`+handle.TaskID()+`","block":false}`), nil)
	if err != nil || notReady.IsError {
		t.Fatalf("not_ready run: %+v err=%v", notReady, err)
	}
	if !strings.Contains(notReady.Content, "<retrieval_status>not_ready</retrieval_status>") {
		t.Fatalf("content = %q", notReady.Content)
	}

	blocked, err := tl.Run(context.Background(), json.RawMessage(`{"task_id":"`+handle.TaskID()+`","block":true,"timeout_ms":10000}`), nil)
	if err != nil || blocked.IsError {
		t.Fatalf("block run: %+v err=%v", blocked, err)
	}
	for _, want := range []string{"<retrieval_status>success</retrieval_status>", "<status>completed</status>", "done-later"} {
		if !strings.Contains(blocked.Content, want) {
			t.Fatalf("content %q missing %q", blocked.Content, want)
		}
	}

	missing, err := tl.Run(context.Background(), json.RawMessage(`{"task_id":"nope","block":false}`), nil)
	if err != nil || !missing.IsError {
		t.Fatalf("missing task = %+v err=%v", missing, err)
	}
}

// TestTaskOutputWaitsWithTimeout 验证阻塞等待的超时路径。
func TestTaskOutputWaitsWithTimeout(t *testing.T) {
	reg, _ := newTestRegistry(t)
	tl := NewTaskOutput(reg)
	handle := reg.StartBash(StartBashInput{Command: "sleep 2"})
	started := time.Now()
	res, err := tl.Run(context.Background(),
		json.RawMessage(`{"task_id":"`+handle.TaskID()+`","block":true,"timeout_ms":300}`), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Content, "<retrieval_status>timeout</retrieval_status>") {
		t.Fatalf("content = %q", res.Content)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("timeout wait overshot")
	}
}

// TestTaskStopTool 验证 TaskStop 工具。
func TestTaskStopTool(t *testing.T) {
	reg, _ := newTestRegistry(t)
	tl := NewTaskStop(reg)
	handle := reg.StartBash(StartBashInput{Command: "sleep 30", Description: "long sleep"})
	res, err := tl.Run(context.Background(), json.RawMessage(`{"task_id":"`+handle.TaskID()+`"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("stop result = %+v err=%v", res, err)
	}
	if !strings.Contains(res.Content, "Successfully stopped task") {
		t.Fatalf("content = %q", res.Content)
	}
	got, _ := reg.Get(handle.TaskID())
	if got.Status != StatusStopped {
		t.Fatalf("status = %s", got.Status)
	}
}

// TestTaskListTool 验证 TaskList 工具。
func TestTaskListTool(t *testing.T) {
	reg, _ := newTestRegistry(t)
	reg.StartBash(StartBashInput{Command: "echo x", Description: "list me"})
	tl := NewTaskList(reg)
	res, err := tl.Run(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("list = %+v err=%v", res, err)
	}
	if !strings.Contains(res.Content, "local_bash") || !strings.Contains(res.Content, "list me") {
		t.Fatalf("content = %q", res.Content)
	}
}

// TestRegisterAgentLifecycle 验证 agent 任务的登记、进度与终态。
func TestRegisterAgentLifecycle(t *testing.T) {
	reg, notify := newTestRegistry(t)
	handle := reg.RegisterAgent(AgentInfo{
		ID:          "subagent_x",
		SessionID:   "s1",
		Description: "research",
		Cancel:      func() {},
	})
	if handle.TaskID() != "subagent_x" {
		t.Fatalf("id = %q", handle.TaskID())
	}
	handle.AppendProgress("tool Read -> done")
	if lines := reg.Progress("subagent_x"); len(lines) != 1 {
		t.Fatalf("progress = %v", lines)
	}

	notReady, err := NewTaskOutput(reg).Run(context.Background(),
		json.RawMessage(`{"task_id":"subagent_x","block":false}`), nil)
	if err != nil {
		t.Fatalf("TaskOutput: %v", err)
	}
	if !strings.Contains(notReady.Content, "<progress>") {
		t.Fatalf("content = %q", notReady.Content)
	}

	handle.Finish(StatusCompleted, "final answer", "")
	got, ok := reg.WaitTerminal("subagent_x", time.Second)
	if !ok || got.Status != StatusCompleted || got.Result != "final answer" {
		t.Fatalf("task = %+v", got)
	}
	select {
	case done := <-notify:
		if done.ID != "subagent_x" {
			t.Fatalf("notification = %+v", done)
		}
	case <-time.After(time.Second):
		t.Fatal("no agent completion notification")
	}

	// 续跑：Restart 回到 running，再次 Finish 落终态。
	handle.Restart()
	state, _ := reg.Get("subagent_x")
	if state.Status != StatusRunning {
		t.Fatalf("status after restart = %s", state.Status)
	}
	handle.Finish(StatusFailed, "", "boom")
	state, _ = reg.Get("subagent_x")
	if state.Status != StatusFailed || state.Error != "boom" {
		t.Fatalf("task after second finish = %+v", state)
	}
}

// TestStopUnknownTask 验证未知任务报错。
func TestStopUnknownTask(t *testing.T) {
	reg, _ := newTestRegistry(t)
	if _, err := reg.Stop("missing"); err == nil {
		t.Fatal("Stop of unknown task should fail")
	}
}

// TestOutputTailTruncatesUTF8 验证尾窗截断不切断多字节字符。
func TestOutputTailTruncatesUTF8(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out")
	payload := strings.Repeat("水", 3000) // 9000 bytes
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	text, truncated, total, err := readTail(path, 1024)
	if err != nil || !truncated || total != int64(len(payload)) {
		t.Fatalf("truncated=%v total=%d err=%v", truncated, total, err)
	}
	if !strings.HasPrefix(payload, text) {
		t.Fatal("tail is not a suffix of the payload (UTF-8 boundary broken)")
	}
}
