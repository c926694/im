-- 迁移程序会从旧账户复制余额；NULL 用于标识尚未填充的用户。
ALTER TABLE users
    ADD COLUMN point_balance BIGINT NULL COMMENT '用户当前可用积分余额';
