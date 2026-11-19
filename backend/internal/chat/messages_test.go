package chat_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/c926694/im/backend/internal/chat"
)

func TestNewMessageDraft(t *testing.T) {
	const id = "7A4AC694-2A74-4AA6-9611-503FFAFC2B15"
	for _, tt := range []struct {
		name    string
		id      string
		content string
		want    error
	}{
		{"valid UTF-8", id, strings.Repeat("聊", 2000), nil},
		{"invalid UUID", "7A4AC694-2A74-4AA6-9611-503FFAFC2B1Z", "你好", chat.ErrInvalidClientMsgID},
		{"blank content", id, " \n\t", chat.ErrInvalidContent},
		{"invalid UTF-8", id, "\xff", chat.ErrInvalidContent},
		{"too long", id, strings.Repeat("聊", 2001), chat.ErrInvalidContent},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := chat.NewMessageDraft(tt.id, tt.content)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewMessageDraft error = %v, want %v", err, tt.want)
			}
		})
	}
}
