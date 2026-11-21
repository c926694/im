SET NAMES utf8mb4;

CREATE TABLE friendships (
    user_low_id BIGINT NOT NULL COMMENT '好友关系中较小的用户 ID，逻辑关联 users.id',
    user_high_id BIGINT NOT NULL COMMENT '好友关系中较大的用户 ID，逻辑关联 users.id',
    request_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '最近一次好友申请的标识，重新申请时生成新值',
    requested_by BIGINT NOT NULL COMMENT '最近一次好友申请的发起人 ID，逻辑关联 users.id',
    status ENUM('pending', 'accepted', 'rejected', 'removed') NOT NULL COMMENT '好友关系状态：待处理、已成为好友、已拒绝、已解除',
    requested_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '最近一次好友申请的发起时间（UTC）',
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '最近一次好友关系状态变更时间（UTC）',
    PRIMARY KEY (user_low_id, user_high_id),
    UNIQUE KEY uq_friendships_request (request_id),
    KEY idx_friendships_low_status (user_low_id, status),
    KEY idx_friendships_high_status (user_high_id, status),
    CONSTRAINT ck_friendships_pair CHECK (user_low_id < user_high_id),
    CONSTRAINT ck_friendships_requester CHECK (requested_by IN (user_low_id, user_high_id))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='用户对的好友关系与最近一次申请';
