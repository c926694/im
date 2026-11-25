-- 所有余额核对通过后，启用余额约束并删除旧账户表。
ALTER TABLE users
    MODIFY COLUMN point_balance BIGINT NOT NULL DEFAULT 0 COMMENT '用户当前可用积分余额',
    ADD CONSTRAINT ck_users_point_balance CHECK (point_balance >= 0);

DROP TABLE point_accounts;
