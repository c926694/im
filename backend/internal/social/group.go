package social

import (
	"context"
	"database/sql"
	"errors"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/jmoiron/sqlx"
)

const MaxGroupMembers = 200

var (
	ErrGroupUnavailable   = errors.New("group unavailable")
	ErrGroupPermission    = errors.New("group role does not allow this action")
	ErrGroupMemberMissing = errors.New("target is not in the group")
	ErrGroupLimit         = errors.New("group member limit exceeded")
	ErrGroupInviteFriends = errors.New("new group members must be the inviter's friends")
	ErrOwnerMustTransfer  = errors.New("owner must transfer ownership before leaving")
)

type GroupChange struct {
	ID         int64
	Recipients []int64
}

type GroupDetails struct {
	Conversation chat.Conversation
	Members      []chat.Member
}

// CreateGroup 在同一个事务中保存会话、群主和受邀成员。
func (s *Service) CreateGroup(ctx context.Context, ownerID int64, name string, invitees []int64) (GroupChange, error) {
	if len(invitees)+1 > MaxGroupMembers {
		return GroupChange{}, ErrGroupLimit
	}
	tx, err := s.conversations.BeginTx(ctx)
	if err != nil {
		return GroupChange{}, err
	}
	defer tx.Rollback()
	if len(invitees) > 0 {
		if err := s.requireInviteFriends(ctx, tx, ownerID, invitees); err != nil {
			return GroupChange{}, err
		}
	}
	id, err := s.conversations.InsertGroup(ctx, tx, name)
	if err != nil {
		return GroupChange{}, err
	}
	if err := s.conversations.AddMembers(ctx, tx, id, 0, chat.Owner, []int64{ownerID}); err != nil {
		return GroupChange{}, err
	}
	if len(invitees) > 0 {
		if err := s.conversations.AddMembers(ctx, tx, id, 0, chat.Normal, invitees); err != nil {
			return GroupChange{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return GroupChange{}, err
	}
	return GroupChange{ID: id, Recipients: append([]int64{ownerID}, invitees...)}, nil
}

// GroupDetails 只向当前群成员返回标题和有效成员名单。
func (s *Service) GroupDetails(ctx context.Context, actorID, id int64) (GroupDetails, error) {
	tx, err := s.conversations.BeginSnapshot(ctx)
	if err != nil {
		return GroupDetails{}, err
	}
	defer tx.Rollback()
	group, err := s.conversations.GetConversation(ctx, tx, id)
	if err := groupAccessError(group, err); err != nil {
		return GroupDetails{}, err
	}
	if _, err := s.chat.RequireMember(ctx, tx, actorID, id); err != nil {
		return GroupDetails{}, err
	}
	members, err := s.conversations.ActiveMembers(ctx, tx, id)
	return GroupDetails{Conversation: group, Members: members}, err
}

// InviteMembers 只添加当前不在群内的好友，已有成员保持原角色和进度。
func (s *Service) InviteMembers(ctx context.Context, actorID, id int64, invitees []int64) (GroupChange, error) {
	return s.mutateGroup(ctx, actorID, id, func(tx *sqlx.Tx, group chat.Conversation, roles map[int64]chat.Role) ([]int64, error) {
		if roles[actorID] == chat.Normal {
			return nil, ErrGroupPermission
		}
		newMembers := make([]int64, 0, len(invitees))
		for _, userID := range invitees {
			if _, inGroup := roles[userID]; !inGroup {
				newMembers = append(newMembers, userID)
			}
		}
		if len(newMembers) == 0 {
			return nil, nil
		}
		if len(roles)+len(newMembers) > MaxGroupMembers {
			return nil, ErrGroupLimit
		}
		if err := s.requireInviteFriends(ctx, tx, actorID, newMembers); err != nil {
			return nil, err
		}
		// 入群起点与消息写入使用同一条会话记录协调。
		return newMembers, s.conversations.AddMembers(ctx, tx, id, group.LastSeq, chat.Normal, newMembers)
	})
}

// RenameGroup 只允许群主更新群名。
func (s *Service) RenameGroup(ctx context.Context, actorID, id int64, name string) (GroupChange, error) {
	return s.mutateGroup(ctx, actorID, id, func(tx *sqlx.Tx, _ chat.Conversation, roles map[int64]chat.Role) ([]int64, error) {
		if roles[actorID] != chat.Owner {
			return nil, ErrGroupPermission
		}
		return nil, s.conversations.RenameGroup(ctx, tx, id, name)
	})
}

// RemoveGroupMember 按操作者与目标的角色限制移除权限。
func (s *Service) RemoveGroupMember(ctx context.Context, actorID, id, targetID int64) (GroupChange, error) {
	return s.mutateGroup(ctx, actorID, id, func(tx *sqlx.Tx, _ chat.Conversation, roles map[int64]chat.Role) ([]int64, error) {
		actorRole, targetRole := roles[actorID], roles[targetID]
		if actorID == targetID || actorRole == chat.Normal || targetRole == chat.Owner || (actorRole == chat.Admin && targetRole == chat.Admin) {
			return nil, ErrGroupPermission
		}
		if targetRole == "" {
			return nil, ErrGroupMemberMissing
		}
		return nil, s.conversations.RemoveMember(ctx, tx, id, targetID)
	})
}

// SetGroupRole 允许群主设置或取消管理员身份。
func (s *Service) SetGroupRole(ctx context.Context, actorID, id, targetID int64, role chat.Role) (GroupChange, error) {
	return s.mutateGroup(ctx, actorID, id, func(tx *sqlx.Tx, _ chat.Conversation, roles map[int64]chat.Role) ([]int64, error) {
		if roles[actorID] != chat.Owner || actorID == targetID {
			return nil, ErrGroupPermission
		}
		if roles[targetID] == "" {
			return nil, ErrGroupMemberMissing
		}
		return nil, s.conversations.SetRole(ctx, tx, id, targetID, role)
	})
}

// TransferGroup 同时更新原群主和接任者的角色，提交时保留一名群主。
func (s *Service) TransferGroup(ctx context.Context, actorID, id, targetID int64) (GroupChange, error) {
	return s.mutateGroup(ctx, actorID, id, func(tx *sqlx.Tx, _ chat.Conversation, roles map[int64]chat.Role) ([]int64, error) {
		if roles[actorID] != chat.Owner || actorID == targetID {
			return nil, ErrGroupPermission
		}
		if roles[targetID] == "" {
			return nil, ErrGroupMemberMissing
		}
		if err := s.conversations.SetRole(ctx, tx, id, actorID, chat.Normal); err != nil {
			return nil, err
		}
		return nil, s.conversations.SetRole(ctx, tx, id, targetID, chat.Owner)
	})
}

// LeaveGroup 仅在最后一名成员退出时关闭群会话。
func (s *Service) LeaveGroup(ctx context.Context, actorID, id int64) (GroupChange, error) {
	return s.mutateGroup(ctx, actorID, id, func(tx *sqlx.Tx, _ chat.Conversation, roles map[int64]chat.Role) ([]int64, error) {
		if roles[actorID] == chat.Owner && len(roles) > 1 {
			return nil, ErrOwnerMustTransfer
		}
		if err := s.conversations.RemoveMember(ctx, tx, id, actorID); err != nil {
			return nil, err
		}
		if len(roles) == 1 {
			return nil, s.conversations.CloseGroup(ctx, tx, id)
		}
		return nil, nil
	})
}

// mutateGroup 在会话行锁内读取角色并执行变更，通知范围包含操作前后在群的成员。
func (s *Service) mutateGroup(ctx context.Context, actorID, id int64,
	apply func(*sqlx.Tx, chat.Conversation, map[int64]chat.Role) ([]int64, error)) (GroupChange, error) {
	tx, err := s.conversations.BeginTx(ctx)
	if err != nil {
		return GroupChange{}, err
	}
	defer tx.Rollback()
	group, err := s.conversations.LockConversation(ctx, tx, id)
	if err := groupAccessError(group, err); err != nil {
		return GroupChange{}, err
	}
	members, err := s.conversations.ActiveMembers(ctx, tx, id)
	if err != nil {
		return GroupChange{}, err
	}
	roles := make(map[int64]chat.Role, len(members))
	recipients := make([]int64, 0, len(members))
	for _, member := range members {
		roles[member.UserID] = member.Role
		recipients = append(recipients, member.UserID)
	}
	if _, inGroup := roles[actorID]; !inGroup {
		return GroupChange{}, chat.ErrNotMember
	}
	added, err := apply(tx, group, roles)
	if err != nil {
		return GroupChange{}, err
	}
	if err := tx.Commit(); err != nil {
		return GroupChange{}, err
	}
	return GroupChange{ID: id, Recipients: append(recipients, added...)}, nil
}

// requireInviteFriends 按批次检查好友资格；邀请无需锁定好友记录。
func (s *Service) requireInviteFriends(ctx context.Context, tx *sqlx.Tx, actorID int64, invitees []int64) error {
	count, err := s.store.countFriends(ctx, tx, actorID, invitees)
	if err != nil {
		return err
	}
	if count != len(invitees) {
		return ErrGroupInviteFriends
	}
	return nil
}

// groupAccessError 统一处理不存在、已关闭和单聊类型的群管理请求。
func groupAccessError(group chat.Conversation, err error) error {
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (group.Kind != chat.Group || group.ClosedAt.Valid)) {
		return ErrGroupUnavailable
	}
	return err
}
