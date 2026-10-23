package session

import (
	"context"
	"errors"
	"strings"
)

// MaxTitleRunes 是会话标题的最大字符数。超出部分在写入前截断。
const MaxTitleRunes = 80

// SetTitle 以追加记录的方式更新会话标题。
// transcript 的首行 session_meta 不可改写，读取时（List/Replay）以最后一条
// title 记录为准；这样标题更新与消息一样是同一条追加式写入路径。
func SetTitle(ctx context.Context, store Store, sessionID, title string) error {
	if store == nil {
		return errors.New("session: nil store")
	}
	if err := ValidateID(sessionID); err != nil {
		return err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("session: empty title")
	}
	if r := []rune(title); len(r) > MaxTitleRunes {
		title = strings.TrimSpace(string(r[:MaxTitleRunes]))
	}
	return store.Append(ctx, Record{
		Type:      RecordTitle,
		SessionID: sessionID,
		Meta:      map[string]string{"title": title},
	})
}
