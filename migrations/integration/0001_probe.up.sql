-- 仅隔离探针；幂等主键支持重复投递验证，不存身份或业务正文。
CREATE TABLE IF NOT EXISTS foundation_probe_receipts (
    probe_id varchar(32) PRIMARY KEY CHECK (probe_id ~ '^[0-9a-f]{32}$'),
    delivery_count integer NOT NULL DEFAULT 1 CHECK (delivery_count > 0),
    consumed_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE foundation_probe_receipts IS '隔离中间件探针消费回执，不存身份或业务正文';
COMMENT ON COLUMN foundation_probe_receipts.probe_id IS '随机探针事件标识，作为重复投递的幂等主键';
COMMENT ON COLUMN foundation_probe_receipts.delivery_count IS '同一探针事件实际完成数据库消费的次数';
COMMENT ON COLUMN foundation_probe_receipts.consumed_at IS '最近一次消费完成时间';
