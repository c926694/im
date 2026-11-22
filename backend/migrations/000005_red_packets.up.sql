SET NAMES utf8mb4;

CREATE TABLE point_accounts (
    user_id BIGINT PRIMARY KEY COMMENT '积分所属用户 ID',
    balance BIGINT NOT NULL COMMENT '可用积分余额',
    CHECK (balance >= 0)
) ENGINE=InnoDB COMMENT='每个用户的可用积分账户';

CREATE TABLE point_ledger (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY COMMENT '积分流水 ID，供倒序分页',
    user_id BIGINT NOT NULL COMMENT '发生变动的用户 ID',
    kind ENUM('grant','send','claim','refund') NOT NULL COMMENT '初始发放、发送扣减、领取入账或到期退回',
    ref_id BIGINT NOT NULL COMMENT '发放时为用户 ID，其余为红包 ID',
    delta BIGINT NOT NULL COMMENT '积分增加为正数，扣减为负数',
    balance_after BIGINT NOT NULL COMMENT '变动后的可用积分',
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '变动时间（UTC）',
    UNIQUE KEY uq_point_ledger_operation (user_id, kind, ref_id),
    KEY idx_point_ledger_user (user_id, id)
) ENGINE=InnoDB COMMENT='不可修改的积分变动记录';

CREATE TABLE red_packets (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY COMMENT '红包 ID',
    sender_id BIGINT NOT NULL COMMENT '发送者用户 ID',
    recipient_id BIGINT NULL COMMENT '单聊指定领取者 ID，群聊为空',
    points_per_share BIGINT NOT NULL COMMENT '每份固定积分',
    share_count INT NOT NULL COMMENT '发出的总份数',
    remaining INT NOT NULL COMMENT '还未领取或退回的份数',
    status ENUM('open','claimed','refunded') NOT NULL DEFAULT 'open' COMMENT '可领取、已领完或已退款',
    expires_at DATETIME(6) NOT NULL COMMENT '领取截止时间（UTC）',
    KEY idx_red_packets_expiry (status, expires_at, id),
    CHECK (points_per_share > 0 AND share_count BETWEEN 1 AND 199 AND remaining BETWEEN 0 AND share_count),
    CHECK ((status = 'open' AND remaining > 0) OR (status <> 'open' AND remaining = 0))
) ENGINE=InnoDB COMMENT='聊天红包的规则、剩余份数和状态';

CREATE TABLE red_packet_claims (
    red_packet_id BIGINT NOT NULL COMMENT '已领取的红包 ID',
    user_id BIGINT NOT NULL COMMENT '领取者用户 ID',
    points BIGINT NOT NULL COMMENT '本次领取的积分',
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '领取时间（UTC）',
    PRIMARY KEY (red_packet_id, user_id)
) ENGINE=InnoDB COMMENT='每位用户领取每个红包的结果';

CREATE TABLE outbox_events (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY COMMENT '事件 ID，作为消息发布键',
    kind ENUM('created','claimed','refunded') NOT NULL COMMENT '红包创建、领取或退款事件',
    red_packet_id BIGINT NOT NULL COMMENT '通知对应的红包 ID',
    attempts INT NOT NULL DEFAULT 0 COMMENT '发布失败次数',
    next_attempt_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT '下次尝试发布时间（UTC）',
    published_at DATETIME(6) NULL COMMENT '成功发布时间（UTC），待发布时为空',
    KEY idx_outbox_pending (published_at, next_attempt_at, id)
) ENGINE=InnoDB COMMENT='与红包业务一起提交的待发布通知';

ALTER TABLE messages
    MODIFY COLUMN content VARCHAR(2000) NOT NULL COMMENT '文本正文或红包会话预览',
    ADD COLUMN kind ENUM('text','red_packet') NOT NULL DEFAULT 'text' COMMENT '文本或红包卡片',
    ADD COLUMN red_packet_id BIGINT NULL COMMENT '卡片对应的红包 ID，文本为空',
    ADD UNIQUE KEY uq_messages_red_packet (red_packet_id),
    ADD CONSTRAINT ck_messages_red_packet CHECK ((kind = 'text' AND red_packet_id IS NULL) OR (kind = 'red_packet' AND red_packet_id IS NOT NULL));

START TRANSACTION;
INSERT INTO point_accounts (user_id, balance) SELECT id, 1000 FROM users;
INSERT INTO point_ledger (user_id, kind, ref_id, delta, balance_after) SELECT user_id, 'grant', user_id, balance, balance FROM point_accounts;
COMMIT;
