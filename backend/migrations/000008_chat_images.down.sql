DROP TABLE message_images;
UPDATE messages SET kind = 'text' WHERE kind = 'image';
ALTER TABLE messages
    MODIFY COLUMN kind ENUM('text','red_packet','red_packet_claim') NOT NULL DEFAULT 'text'
        COMMENT '文本消息、红包卡片或红包领取通知';
