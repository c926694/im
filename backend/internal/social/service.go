package social

import "github.com/c926694/im/backend/internal/chat"

// Service 组织好友、群管理和红包业务，群会话与成员数据仍由 chat.Store 读写。
type Service struct {
	store         *Store
	chat          *chat.Service
	conversations chat.Store
}

var _ chat.FriendshipChecker = (*Store)(nil)

func NewService(store *Store, chats *chat.Service, conversations chat.Store) *Service {
	return &Service{store: store, chat: chats, conversations: conversations}
}
