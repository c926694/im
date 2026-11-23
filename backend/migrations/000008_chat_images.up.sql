ALTER TABLE messages
    MODIFY COLUMN kind ENUM('text','red_packet','red_packet_claim','image') NOT NULL DEFAULT 'text'
        COMMENT '文本、红包卡片、红包领取提示或图片',
    DROP CHECK ck_messages_red_packet,
    ADD CONSTRAINT ck_messages_red_packet CHECK ((kind = 'red_packet') = (red_packet_id IS NOT NULL));

CREATE TABLE message_images (
    message_id BIGINT NOT NULL PRIMARY KEY COMMENT '对应的聊天消息 ID',
    mime_type VARCHAR(32) NOT NULL COMMENT '图片的 MIME 类型',
    content MEDIUMBLOB NOT NULL COMMENT '原始图片数据',
    digest BINARY(32) NOT NULL COMMENT '图片的 SHA-256 摘要，用于比较重试内容'
) ENGINE=InnoDB COMMENT='图片消息的独立内容记录';
