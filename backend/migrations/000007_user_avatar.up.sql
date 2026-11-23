ALTER TABLE users ADD COLUMN avatar_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '用户上传的头像文件名，空字符串表示使用默认头像';
