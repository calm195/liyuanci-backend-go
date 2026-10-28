CREATE TABLE public.interaction_block_policy (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 opaque_enabled boolean NOT NULL DEFAULT false,
 switched_at timestamptz,
 CHECK (opaque_enabled = (switched_at IS NOT NULL))
);
COMMENT ON TABLE public.interaction_block_policy IS '独立于树洞开关的全局匿名黑名单单向切换策略';
COMMENT ON COLUMN public.interaction_block_policy.singleton IS '唯一全局策略行';
COMMENT ON COLUMN public.interaction_block_policy.opaque_enabled IS '启用后禁止回退目标账号标识接口';
COMMENT ON COLUMN public.interaction_block_policy.switched_at IS '已验证历史随机标识后显式切换时间';
INSERT INTO public.interaction_block_policy(singleton) VALUES(true);
CREATE FUNCTION public.protect_interaction_block_policy() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR (OLD.opaque_enabled AND NOT NEW.opaque_enabled) THEN
 RAISE EXCEPTION '匿名黑名单切换不可撤销';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER interaction_block_policy_monotonic BEFORE UPDATE OR DELETE ON public.interaction_block_policy FOR EACH ROW EXECUTE FUNCTION public.protect_interaction_block_policy();

-- 最小清理入口，普通API不获得原稿、审核决定或扫描记录的直接更新/删除权限。
CREATE FUNCTION public.clear_held_whisper_comment(p_token bytea,p_id uuid,p_versions jsonb) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE actor uuid;
BEGIN
 SELECT a.id INTO actor FROM public.sessions s JOIN public.accounts a ON a.id=s.account_id
 WHERE s.token_hash=p_token AND s.subject_kind='student' AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()
 AND s.auth_epoch=a.auth_epoch AND a.lifecycle='active';
 IF actor IS NULL OR NOT EXISTS(SELECT 1 FROM public.whisper_comments c WHERE c.id=p_id AND c.author_id=actor
 AND (c.post_id IS NULL OR EXISTS(SELECT 1 FROM public.privacy_holds h WHERE h.target_kind='whisper_comment' AND h.target_id=c.id AND h.status='active')))
 THEN RAISE EXCEPTION '当前会话不可清理此评论' USING ERRCODE='42501'; END IF;
 IF jsonb_typeof(p_versions)<>'array' OR jsonb_array_length(p_versions)<>(SELECT count(*) FROM public.whisper_comment_versions WHERE comment_id=p_id)
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(p_versions) x WHERE jsonb_typeof(x->'body_cipher')<>'object' OR jsonb_typeof(x->'identity_cipher')<>'object')
 THEN RAISE EXCEPTION '清理信封无效'; END IF;
 UPDATE public.whisper_comment_versions v SET body_cipher=x.body_cipher,identity_cipher=x.identity_cipher,nickname_version=NULL,
 candidate_cipher=NULL,public_body=NULL,public_identity=NULL,review_result_cipher=NULL
 FROM jsonb_to_recordset(p_versions) AS x(content_version bigint,body_cipher jsonb,identity_cipher jsonb)
 WHERE v.comment_id=p_id AND v.content_version=x.content_version;
 DELETE FROM public.moderation_scans WHERE task_id IN (SELECT t.id FROM public.moderation_tasks t JOIN public.content_registry r ON r.id=t.content_id WHERE r.kind='whisper_comment' AND r.entity_id=p_id);
 UPDATE public.moderation_tasks SET reason=CASE WHEN decision='rejected' THEN '普通副本已清理' ELSE NULL END,revision_suggestion=NULL
 WHERE content_id IN (SELECT id FROM public.content_registry WHERE kind='whisper_comment' AND entity_id=p_id);
 UPDATE public.whisper_comments SET recovery_cipher=NULL WHERE id=p_id;
END $$;
COMMENT ON FUNCTION public.clear_held_whisper_comment(bytea,uuid,jsonb) IS '经有效会话仅清除本人受隐私保护评论的普通密文和审核片段，不解除屏障或改变审核决定';
REVOKE ALL ON FUNCTION public.clear_held_whisper_comment(bytea,uuid,jsonb) FROM PUBLIC;

ALTER TABLE public.whisper_notification_refs ADD COLUMN suppressed boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN public.whisper_notification_refs.suppressed IS '隐私受理后抑制读取与投递，技术定位保留至外部清理确认';
