CREATE TABLE users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    username VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    nickname VARCHAR(64) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE conversations (
    id BIGINT NOT NULL AUTO_INCREMENT,
    direct_user_low_id BIGINT NOT NULL,
    direct_user_high_id BIGINT NOT NULL,
    last_seq BIGINT NOT NULL DEFAULT 0,
    last_activity_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_conversations_direct_pair (direct_user_low_id, direct_user_high_id),
    CONSTRAINT ck_conversations_direct_pair CHECK (direct_user_low_id < direct_user_high_id),
    CONSTRAINT ck_conversations_last_seq CHECK (last_seq >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE conversation_members (
    conversation_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    last_read_seq BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (conversation_id, user_id),
    KEY idx_conversation_members_user (user_id, conversation_id),
    CONSTRAINT ck_conversation_members_read_seq CHECK (last_read_seq >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE messages (
    id BIGINT NOT NULL AUTO_INCREMENT,
    conversation_id BIGINT NOT NULL,
    sender_id BIGINT NOT NULL,
    client_msg_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    seq BIGINT NOT NULL,
    content VARCHAR(2000) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_messages_conversation_seq (conversation_id, seq),
    UNIQUE KEY uq_messages_client_msg (conversation_id, sender_id, client_msg_id),
    CONSTRAINT ck_messages_seq CHECK (seq > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
