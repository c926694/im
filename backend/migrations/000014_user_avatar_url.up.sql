ALTER TABLE users
    CHANGE COLUMN avatar_name avatar_url VARCHAR(255) NOT NULL DEFAULT '/avatars/default.webp'
    COMMENT '用户头像的站内相对访问路径，默认使用系统头像';

-- 补齐公开访问路径，头像文件继续使用原来的名称和存储目录。
UPDATE users
SET avatar_url = CONCAT('/avatars/', COALESCE(NULLIF(avatar_url, ''), 'default.webp'));
