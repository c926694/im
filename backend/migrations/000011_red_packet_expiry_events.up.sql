ALTER TABLE outbox_events
    MODIFY COLUMN kind ENUM('created','claimed','refunded','message_created','packet_expiry') NOT NULL
        COMMENT '聊天通知、红包通知或红包到期任务',
    ADD COLUMN deliver_at DATETIME(6) NULL COMMENT 'RocketMQ 到期任务的绝对投递时间（UTC），普通通知为空';

INSERT INTO outbox_events (kind, red_packet_id, deliver_at)
    SELECT 'packet_expiry', id, expires_at FROM red_packets WHERE status = 'open';
