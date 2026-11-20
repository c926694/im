-- 注册账号。数据库字段备注由 000002_direct_chat_comments 写入。
CREATE TABLE users (
    -- 用户主键，供会话、成员和消息引用。
    id BIGINT NOT NULL AUTO_INCREMENT,
    -- 唯一用户名，用于登录和查找聊天对象。
    username VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    -- 登录密码的哈希。
    password_hash VARCHAR(255) NOT NULL,
    -- 会话中显示的昵称，可以重复。
    nickname VARCHAR(64) NOT NULL,
    -- 账号注册时间，使用 UTC。
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 两个用户之间的一段单聊。
CREATE TABLE conversations (
    -- 会话主键，供成员和消息引用。
    id BIGINT NOT NULL AUTO_INCREMENT,
    -- 双方中较小的 users.id，与较大的 ID 组成唯一用户对。
    direct_user_low_id BIGINT NOT NULL,
    -- 双方中较大的 users.id，与较小的 ID 组成唯一用户对。
    direct_user_high_id BIGINT NOT NULL,
    -- 最新消息序号，0 表示会话还没有消息。
    last_seq BIGINT NOT NULL DEFAULT 0,
    -- 最近活动时间，用于会话列表排序，使用 UTC。
    last_activity_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    -- 会话创建时间，使用 UTC。
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_conversations_direct_pair (direct_user_low_id, direct_user_high_id),
    CONSTRAINT ck_conversations_direct_pair CHECK (direct_user_low_id < direct_user_high_id),
    CONSTRAINT ck_conversations_last_seq CHECK (last_seq >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 用户参加的会话，用于访问权限和个人已读进度。
CREATE TABLE conversation_members (
    -- 所属会话，对应 conversations.id。
    conversation_id BIGINT NOT NULL,
    -- 会话成员，对应 users.id。
    user_id BIGINT NOT NULL,
    -- 该成员已读到的消息序号，0 表示尚未读到消息。
    last_read_seq BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (conversation_id, user_id),
    KEY idx_conversation_members_user (user_id, conversation_id),
    CONSTRAINT ck_conversation_members_read_seq CHECK (last_read_seq >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 已保存的单聊文本消息。
CREATE TABLE messages (
    -- 全局消息主键。
    id BIGINT NOT NULL AUTO_INCREMENT,
    -- 所属会话，对应 conversations.id。
    conversation_id BIGINT NOT NULL,
    -- 发送者，对应 users.id。
    sender_id BIGINT NOT NULL,
    -- 客户端发送 UUID，重试时复用，用于避免重复保存。
    client_msg_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    -- 会话内消息序号，从 1 开始，用于排序和分页。
    seq BIGINT NOT NULL,
    -- 文本消息正文。
    content VARCHAR(2000) NOT NULL,
    -- 消息创建时间，使用 UTC。
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_messages_conversation_seq (conversation_id, seq),
    UNIQUE KEY uq_messages_client_msg (conversation_id, sender_id, client_msg_id),
    CONSTRAINT ck_messages_seq CHECK (seq > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
