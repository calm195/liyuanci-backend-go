-- 后台会话RLS继续隐藏学生行；仅允许验证已持有令牌的领域，不返回任何身份或会话字段。
CREATE FUNCTION public.is_live_student_session(token_digest bytea)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog
AS $$
 SELECT EXISTS (
   SELECT 1 FROM public.sessions s JOIN public.accounts a ON a.id=s.account_id
   WHERE s.subject_kind='student' AND s.token_hash=token_digest
     AND s.revoked_at IS NULL AND s.expires_at>statement_timestamp()
     AND a.lifecycle='active' AND a.auth_epoch=s.auth_epoch
 )
$$;
COMMENT ON FUNCTION public.is_live_student_session(bytea) IS '仅验证已持有摘要是否为有效学生会话，后台跨域返回拒绝且不披露身份或会话资料';
REVOKE ALL ON FUNCTION public.is_live_student_session(bytea) FROM PUBLIC;
