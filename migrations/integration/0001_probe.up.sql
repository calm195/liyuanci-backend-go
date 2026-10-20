-- 仅隔离探针；幂等主键支持重复投递验证，不存身份或业务正文。
CREATE TABLE IF NOT EXISTS foundation_probe_receipts (
    probe_id varchar(32) PRIMARY KEY CHECK (probe_id ~ '^[0-9a-f]{32}$'),
    delivery_count integer NOT NULL DEFAULT 1 CHECK (delivery_count > 0),
    consumed_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
