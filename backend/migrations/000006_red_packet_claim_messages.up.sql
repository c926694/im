SET NAMES utf8mb4;

ALTER TABLE messages
    DROP CHECK ck_messages_red_packet,
    MODIFY COLUMN kind ENUM('text','red_packet','red_packet_claim') NOT NULL DEFAULT 'text'
        COMMENT '文本消息、红包卡片或红包领取通知',
    ADD CONSTRAINT ck_messages_red_packet CHECK (
        (kind = 'text' AND red_packet_id IS NULL)
        OR (kind = 'red_packet' AND red_packet_id IS NOT NULL)
        OR (kind = 'red_packet_claim' AND red_packet_id IS NULL)
    );

ALTER TABLE outbox_events
    ADD COLUMN message_id BIGINT NULL COMMENT '领取通知对应的聊天消息 ID';
