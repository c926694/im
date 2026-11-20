SET NAMES utf8mb4;

ALTER TABLE users
    COMMENT = '注册账号',
    MODIFY COLUMN id BIGINT NOT NULL AUTO_INCREMENT COMMENT '用户主键，供会话、成员和消息引用',
    MODIFY COLUMN username VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '唯一用户名，用于登录和查找聊天对象',
    MODIFY COLUMN password_hash VARCHAR(255) NOT NULL COMMENT '登录密码的哈希',
    MODIFY COLUMN nickname VARCHAR(64) NOT NULL COMMENT '会话中显示的昵称，可以重复',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '账号注册时间，使用 UTC';

ALTER TABLE conversations
    COMMENT = '两个用户之间的一段单聊',
    MODIFY COLUMN id BIGINT NOT NULL AUTO_INCREMENT COMMENT '会话主键，供成员和消息引用',
    MODIFY COLUMN direct_user_low_id BIGINT NOT NULL COMMENT '双方中较小的 users.id，与较大的 ID 组成唯一用户对',
    MODIFY COLUMN direct_user_high_id BIGINT NOT NULL COMMENT '双方中较大的 users.id，与较小的 ID 组成唯一用户对',
    MODIFY COLUMN last_seq BIGINT NOT NULL DEFAULT 0 COMMENT '最新消息序号，0 表示会话还没有消息',
    MODIFY COLUMN last_activity_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '最近活动时间，用于会话列表排序，使用 UTC',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '会话创建时间，使用 UTC';

ALTER TABLE conversation_members
    COMMENT = '用户参加的会话，用于访问权限和个人已读进度',
    MODIFY COLUMN conversation_id BIGINT NOT NULL COMMENT '所属会话，对应 conversations.id',
    MODIFY COLUMN user_id BIGINT NOT NULL COMMENT '会话成员，对应 users.id',
    MODIFY COLUMN last_read_seq BIGINT NOT NULL DEFAULT 0 COMMENT '该成员已读到的消息序号，0 表示尚未读到消息';

ALTER TABLE messages
    COMMENT = '已保存的单聊文本消息',
    MODIFY COLUMN id BIGINT NOT NULL AUTO_INCREMENT COMMENT '全局消息主键',
    MODIFY COLUMN conversation_id BIGINT NOT NULL COMMENT '所属会话，对应 conversations.id',
    MODIFY COLUMN sender_id BIGINT NOT NULL COMMENT '发送者，对应 users.id',
    MODIFY COLUMN client_msg_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '客户端发送 UUID，重试时复用，用于避免重复保存',
    MODIFY COLUMN seq BIGINT NOT NULL COMMENT '会话内消息序号，从 1 开始，用于排序和分页',
    MODIFY COLUMN content VARCHAR(2000) NOT NULL COMMENT '文本消息正文',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '消息创建时间，使用 UTC';
