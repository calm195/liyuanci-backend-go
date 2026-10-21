-- 一次性code只存用途隔离摘要；完成回执通过会话外键随主体清理，不保存code或令牌明文。
CREATE TABLE public.student_login_exchanges (
 id uuid PRIMARY KEY,
 code_hash bytea NOT NULL UNIQUE CHECK (octet_length(code_hash)=32),
 request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
 nonce bytea NOT NULL CHECK (octet_length(nonce)=32),
 status text NOT NULL DEFAULT 'processing' CHECK (status IN ('processing','completed')),
 session_id uuid REFERENCES public.sessions(id) ON DELETE CASCADE,
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 CHECK ((status='completed')=(session_id IS NOT NULL))
);
COMMENT ON TABLE public.student_login_exchanges IS '学生微信一次性交换去重及短期会话引用回执，不保存微信code、身份或令牌';
COMMENT ON COLUMN public.student_login_exchanges.id IS '随机交换回执标识';
COMMENT ON COLUMN public.student_login_exchanges.code_hash IS '用途隔离HMAC的微信code摘要，唯一限制一次交换';
COMMENT ON COLUMN public.student_login_exchanges.request_hash IS '用途隔离HMAC的客户端请求标识摘要，防止跨请求重放';
COMMENT ON COLUMN public.student_login_exchanges.nonce IS '独立随机会话派生材料，单独持有不能恢复令牌';
COMMENT ON COLUMN public.student_login_exchanges.status IS '交换处理中或已原子提交身份及会话的完成状态';
COMMENT ON COLUMN public.student_login_exchanges.session_id IS '完成后绑定的学生会话，随会话物理清理级联删除';
COMMENT ON COLUMN public.student_login_exchanges.expires_at IS '同code成功回执绝对有效期，到期必须重新获取code';
COMMENT ON COLUMN public.student_login_exchanges.created_at IS '数据库记录的领取时间';
