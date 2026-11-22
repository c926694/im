SET NAMES utf8mb4;

ALTER TABLE messages
    ADD CONSTRAINT ck_messages_text_only CHECK (kind = 'text');
ALTER TABLE messages
    DROP CHECK ck_messages_text_only,
    DROP CHECK ck_messages_red_packet,
    DROP INDEX uq_messages_red_packet,
    DROP COLUMN red_packet_id,
    DROP COLUMN kind,
    MODIFY COLUMN content VARCHAR(2000) NOT NULL COMMENT '文本消息正文';
DROP TABLE outbox_events, red_packet_claims, red_packets, point_ledger, point_accounts;
