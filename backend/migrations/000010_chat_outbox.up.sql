ALTER TABLE outbox_events
    MODIFY COLUMN kind ENUM('created','claimed','refunded','message_created') NOT NULL
        COMMENT '红包创建、领取、退款或普通聊天消息创建',
    MODIFY COLUMN red_packet_id BIGINT NULL COMMENT '红包事件对应的红包 ID，普通消息事件为空',
    MODIFY COLUMN message_id BIGINT NULL COMMENT '普通消息或红包领取通知对应的聊天消息 ID';
