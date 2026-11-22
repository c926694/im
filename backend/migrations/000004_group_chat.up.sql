SET NAMES utf8mb4;

ALTER TABLE conversations
    COMMENT = '单聊或群聊会话，保存最新消息的序号和时间',
    DROP CHECK ck_conversations_direct_pair,
    MODIFY COLUMN direct_user_low_id BIGINT NULL COMMENT '单聊中较小的用户 ID，群聊为空',
    MODIFY COLUMN direct_user_high_id BIGINT NULL COMMENT '单聊中较大的用户 ID，群聊为空',
    ADD COLUMN kind ENUM('direct', 'group') NOT NULL DEFAULT 'direct' COMMENT '会话类型：单聊或群聊',
    ADD COLUMN group_name VARCHAR(64) NULL COMMENT '群聊名称，单聊为空',
    ADD COLUMN closed_at DATETIME(6) NULL COMMENT '群聊关闭时间（UTC），开放时为空',
    ADD CONSTRAINT ck_conversations_kind CHECK (
        (kind = 'direct' AND direct_user_low_id IS NOT NULL AND direct_user_high_id IS NOT NULL
            AND direct_user_low_id < direct_user_high_id AND group_name IS NULL AND closed_at IS NULL)
        OR (kind = 'group' AND direct_user_low_id IS NULL AND direct_user_high_id IS NULL AND group_name IS NOT NULL)
    );

ALTER TABLE conversation_members
    COMMENT = '会话成员资格、群内角色、可读范围及个人已读进度',
    ADD COLUMN role ENUM('owner', 'admin', 'member') NOT NULL DEFAULT 'member' COMMENT '群内角色：群主、管理员或普通成员',
    ADD COLUMN joined_seq BIGINT NOT NULL DEFAULT 0 COMMENT '本次加入时的最新序号，只能读取其后的消息',
    ADD COLUMN joined_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '本次加入时间（UTC）',
    ADD COLUMN left_at DATETIME(6) NULL COMMENT '退出或被移除的时间（UTC），在会话内时为空',
    ADD CONSTRAINT ck_conversation_members_joined_seq CHECK (joined_seq >= 0 AND last_read_seq >= joined_seq),
    DROP INDEX idx_conversation_members_user,
    ADD INDEX idx_conversation_members_user (user_id, left_at, conversation_id);

ALTER TABLE messages COMMENT = '已保存的单聊和群聊文本消息';
