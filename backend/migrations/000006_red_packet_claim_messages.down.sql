SET NAMES utf8mb4;

ALTER TABLE outbox_events DROP COLUMN message_id;

UPDATE messages SET kind = 'text' WHERE kind = 'red_packet_claim';

ALTER TABLE messages
    DROP CHECK ck_messages_red_packet,
    MODIFY COLUMN kind ENUM('text','red_packet') NOT NULL DEFAULT 'text'
        COMMENT '文本或红包卡片',
    ADD CONSTRAINT ck_messages_red_packet CHECK (
        (kind = 'text' AND red_packet_id IS NULL)
        OR (kind = 'red_packet' AND red_packet_id IS NOT NULL)
    );
