SET NAMES utf8mb4;

-- 群聊记录仍存在时，单聊用户字段恢复为非空的操作会拒绝执行。
ALTER TABLE conversations
    COMMENT = '两个用户之间的一段单聊',
    DROP CHECK ck_conversations_kind,
    MODIFY COLUMN direct_user_low_id BIGINT NOT NULL COMMENT '双方中较小的 users.id，与较大的 ID 组成唯一用户对',
    MODIFY COLUMN direct_user_high_id BIGINT NOT NULL COMMENT '双方中较大的 users.id，与较小的 ID 组成唯一用户对',
    DROP COLUMN kind,
    DROP COLUMN group_name,
    DROP COLUMN closed_at,
    ADD CONSTRAINT ck_conversations_direct_pair CHECK (direct_user_low_id < direct_user_high_id);

ALTER TABLE conversation_members
    COMMENT = '用户参加的会话，用于访问权限和个人已读进度',
    DROP CHECK ck_conversation_members_joined_seq,
    DROP COLUMN role,
    DROP COLUMN joined_seq,
    DROP COLUMN joined_at,
    DROP COLUMN left_at,
    DROP INDEX idx_conversation_members_user,
    ADD INDEX idx_conversation_members_user (user_id, conversation_id);

ALTER TABLE messages COMMENT = '已保存的单聊文本消息';
