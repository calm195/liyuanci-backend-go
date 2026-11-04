-- 会话检查、资源锁和受限内容投影函数；版本 SQL 是结构定义的唯一来源。

CREATE FUNCTION public.clear_held_whisper_comment(p_token bytea, p_id uuid, p_versions jsonb) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
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
COMMENT ON FUNCTION public.clear_held_whisper_comment(p_token bytea, p_id uuid, p_versions jsonb) IS '经有效会话仅清除本人受隐私保护评论的普通密文和审核片段，不解除屏障或改变审核决定';

CREATE FUNCTION public.is_live_admin_session(token_digest bytea) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog'
    AS $$ SELECT EXISTS(SELECT 1 FROM public.sessions s JOIN public.admin_accounts a ON a.id=s.admin_id WHERE s.token_hash=token_digest AND s.subject_kind='admin' AND a.status='active' AND a.auth_epoch=s.auth_epoch AND s.revoked_at IS NULL AND s.expires_at>statement_timestamp()) $$;
COMMENT ON FUNCTION public.is_live_admin_session(token_digest bytea) IS '仅识别已持有有效后台会话的跨域拒绝，不暴露身份';

CREATE FUNCTION public.is_live_student_session(token_digest bytea) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog'
    AS $$
 SELECT EXISTS (
   SELECT 1 FROM public.sessions s JOIN public.accounts a ON a.id=s.account_id
   WHERE s.subject_kind='student' AND s.token_hash=token_digest
     AND s.revoked_at IS NULL AND s.expires_at>statement_timestamp()
     AND a.lifecycle='active' AND a.auth_epoch=s.auth_epoch
 )
$$;
COMMENT ON FUNCTION public.is_live_student_session(token_digest bytea) IS '仅验证已持有摘要是否为有效学生会话，后台跨域返回拒绝且不披露身份或会话资料';

CREATE FUNCTION public.lock_command_account(subject uuid) RETURNS TABLE(id uuid, lifecycle text, verification text, auth_epoch bigint, version bigint)
    LANGUAGE sql
    SET search_path TO 'pg_catalog'
    AS $$
    SELECT a.id, a.lifecycle, a.verification, a.auth_epoch, a.version
    FROM public.accounts a WHERE a.id = subject FOR UPDATE
$$;
COMMENT ON FUNCTION public.lock_command_account(subject uuid) IS '锁定学生账号状态及授权代次，不跨事务缓存授权结论';

CREATE FUNCTION public.lock_command_admin(subject uuid) RETURNS TABLE(id uuid, status text, auth_epoch bigint)
    LANGUAGE sql
    SET search_path TO 'pg_catalog'
    AS $$
    SELECT a.id, a.status, a.auth_epoch
    FROM public.admin_accounts a WHERE a.id = subject FOR UPDATE
$$;
COMMENT ON FUNCTION public.lock_command_admin(subject uuid) IS '锁定后台账号授权元数据，岗位权限由调用方在同一事务复核';

CREATE FUNCTION public.protect_interaction_block_policy() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
 IF TG_OP='DELETE' OR (OLD.opaque_enabled AND NOT NEW.opaque_enabled) THEN
 RAISE EXCEPTION '匿名黑名单切换不可撤销';
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION public.read_whisper_crisis_payload(token_digest bytea, task_ref uuid, report_ref uuid) RETURNS TABLE(body jsonb)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 WITH authorized AS (
 SELECT DISTINCT c.*,t.id AS task_id FROM public.sessions se JOIN public.admin_accounts a ON a.id=se.admin_id JOIN public.admin_role_bindings rb ON rb.admin_id=a.id
 CROSS JOIN public.moderation_tasks t JOIN public.content_registry c ON c.id=t.content_id
 JOIN public.moderation_scans s ON s.task_id=t.id
 WHERE se.token_hash=token_digest AND se.subject_kind='admin' AND se.revoked_at IS NULL AND se.expires_at>clock_timestamp() AND se.auth_epoch=a.auth_epoch AND a.status='active' AND t.id=task_ref
 AND t.status NOT IN ('cancelled','obsolete')
 AND EXISTS(SELECT 1 FROM public.accounts u WHERE u.id=c.author_id AND u.lifecycle='active' AND u.auth_epoch=t.subject_epoch)
 AND NOT EXISTS(SELECT 1 FROM public.privacy_holds h WHERE h.target_kind=c.kind AND (h.target_id=c.entity_id OR h.target_id=c.id) AND h.status='active')
 AND ((report_ref IS NULL AND rb.role='super_admin' AND NOT s.crisis_hit AND (s.ordinary_hit OR s.school_related)) OR (report_ref IS NOT NULL AND rb.role IN ('crisis_operator','super_admin') AND EXISTS(SELECT 1 FROM public.reports r WHERE r.id=report_ref AND r.content_id=c.id AND r.target_kind IN ('whisper_post','whisper_comment'))))
 )
 SELECT v.body_cipher FROM authorized a JOIN public.whisper_post_versions v ON a.kind='whisper_post' AND v.content_registry_id=a.id JOIN public.whisper_posts p ON p.id=v.post_id WHERE p.lifecycle='active' AND p.current_content_version=v.content_version AND v.review_task_id=a.task_id
 UNION ALL SELECT v.body_cipher FROM authorized a JOIN public.whisper_comment_versions v ON a.kind='whisper_comment' AND v.content_registry_id=a.id JOIN public.whisper_comments p ON p.id=v.comment_id WHERE p.lifecycle='active' AND p.content_version=v.content_version AND v.review_task_id=a.task_id
 UNION ALL SELECT v.label_cipher FROM authorized a JOIN public.whisper_nickname_versions v ON a.kind='whisper_nickname' AND v.content_registry_id=a.id WHERE v.review_task_id=a.task_id
 UNION ALL SELECT v.body_cipher FROM authorized a JOIN public.whisper_capsules v ON a.kind='whisper_capsule' AND v.content_registry_id=a.id WHERE v.lifecycle='active' AND v.review_task_id=a.task_id
$$;
COMMENT ON FUNCTION public.read_whisper_crisis_payload(token_digest bytea, task_ref uuid, report_ref uuid) IS '有权危机升格来源的精确原稿投影；普通案件升格仅限同时具备两域权限的超管';

CREATE FUNCTION public.read_whisper_review_payload(token_digest bytea, task_ref uuid) RETURNS TABLE(body jsonb)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 WITH authorized AS (
 SELECT t.id,c.kind,c.entity_id,c.id AS registry_id,c.content_version
 FROM public.sessions se JOIN public.admin_accounts a ON a.id=se.admin_id
 JOIN public.admin_role_bindings rb ON rb.admin_id=a.id AND rb.role IN ('review_operator','super_admin')
 CROSS JOIN public.moderation_tasks t JOIN public.content_registry c ON c.id=t.content_id
 JOIN public.moderation_scans s ON s.task_id=t.id
 JOIN public.accounts u ON u.id=c.author_id
 WHERE se.subject_kind='admin' AND se.token_hash=token_digest AND se.revoked_at IS NULL
 AND se.expires_at>clock_timestamp() AND se.auth_epoch=a.auth_epoch AND a.status='active'
 AND t.id=task_ref AND u.lifecycle='active' AND u.auth_epoch=t.subject_epoch
 AND NOT s.crisis_hit AND (s.school_related OR s.ordinary_hit)
 AND t.status IN ('human_required','approved','rejected')
 AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry cr ON cr.id=x.content_id WHERE x.active AND x.kind='crisis' AND cr.kind=c.kind AND cr.entity_id=c.entity_id)
 AND NOT EXISTS(SELECT 1 FROM public.privacy_holds h WHERE h.target_kind=c.kind AND (h.target_id=c.entity_id OR h.target_id=c.id) AND h.status='active')
 )
 SELECT v.body_cipher FROM authorized a JOIN public.whisper_post_versions v ON a.kind='whisper_post' AND v.content_registry_id=a.registry_id JOIN public.whisper_posts p ON p.id=v.post_id AND p.current_content_version=v.content_version WHERE p.lifecycle='active' AND v.review_task_id=a.id
 UNION ALL SELECT v.body_cipher FROM authorized a JOIN public.whisper_comment_versions v ON a.kind='whisper_comment' AND v.content_registry_id=a.registry_id JOIN public.whisper_comments p ON p.id=v.comment_id AND p.content_version=v.content_version WHERE p.lifecycle='active' AND v.review_task_id=a.id
 UNION ALL SELECT v.label_cipher FROM authorized a JOIN public.whisper_nickname_versions v ON a.kind='whisper_nickname' AND v.content_registry_id=a.registry_id WHERE v.review_task_id=a.id
 UNION ALL SELECT v.body_cipher FROM authorized a JOIN public.whisper_capsules v ON a.kind='whisper_capsule' AND v.content_registry_id=a.registry_id WHERE v.lifecycle='active' AND v.review_task_id=a.id
$$;
COMMENT ON FUNCTION public.read_whisper_review_payload(token_digest bytea, task_ref uuid) IS '当前普通命中树洞案件的单份原稿授权投影；不返回身份或内部账号';

CREATE FUNCTION public.whisper_public_report_body(source_kind text, source_id uuid) RETURNS TABLE(body text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 WITH visible AS (
 SELECT c.id,c.kind,c.entity_id,c.author_id,c.content_version FROM public.content_registry c JOIN public.accounts a ON a.id=c.author_id
 WHERE c.kind=source_kind AND c.entity_id=source_id AND c.kind IN ('whisper_post','whisper_comment') AND a.lifecycle='active' AND (c.kind='whisper_comment' OR a.verification='verified')
 AND c.status='approved' AND c.school_detection_state='completed'
 AND NOT EXISTS(SELECT 1 FROM public.penalties p WHERE p.account_id=a.id AND p.revoked_at IS NULL AND p.action_kind IN ('mute','ban') AND p.starts_at<=clock_timestamp() AND (p.ends_at IS NULL OR p.ends_at>clock_timestamp()))
 AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry cr ON cr.id=x.content_id WHERE x.active AND cr.kind=c.kind AND cr.entity_id=c.entity_id)
 AND NOT EXISTS(SELECT 1 FROM public.privacy_holds h WHERE h.target_kind=c.kind AND h.target_id=c.entity_id AND h.status='active')
 )
 SELECT v.public_body FROM visible c JOIN public.whisper_posts p ON c.kind='whisper_post' AND p.id=c.entity_id AND p.current_content_version=c.content_version JOIN public.whisper_post_versions v ON v.content_registry_id=c.id
 WHERE p.lifecycle='active' AND p.publish_intent AND p.published_content_version=c.content_version AND v.review_state='approved' AND v.public_body IS NOT NULL
 AND (v.nickname_version IS NULL OR EXISTS(SELECT 1 FROM public.whisper_nickname_versions n WHERE n.account_id=p.owner_id AND n.nickname_version=v.nickname_version AND n.review_state='approved' AND n.revoked_at IS NULL AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x WHERE x.content_id=n.content_registry_id AND x.active)))
 UNION ALL
 SELECT v.public_body FROM visible c JOIN public.whisper_comments p ON c.kind='whisper_comment' AND p.id=c.entity_id AND p.content_version=c.content_version JOIN public.whisper_comment_versions v ON v.content_registry_id=c.id JOIN public.whisper_posts parent ON parent.id=p.post_id JOIN public.accounts pa ON pa.id=parent.owner_id JOIN public.whisper_post_versions pv ON pv.post_id=parent.id AND pv.content_version=parent.current_content_version
 WHERE p.lifecycle='active' AND p.review_state='approved' AND v.public_body IS NOT NULL AND parent.lifecycle='active' AND parent.publish_intent AND parent.published_content_version=parent.current_content_version AND parent.current_content_version=p.parent_content_version AND pv.review_state='approved' AND pv.public_body IS NOT NULL AND pa.lifecycle='active' AND pa.verification='verified'
 AND NOT EXISTS(SELECT 1 FROM public.penalties pn WHERE pn.account_id=pa.id AND pn.revoked_at IS NULL AND pn.action_kind IN ('mute','ban') AND pn.starts_at<=clock_timestamp() AND (pn.ends_at IS NULL OR pn.ends_at>clock_timestamp()))
 AND (pv.nickname_version IS NULL OR EXISTS(SELECT 1 FROM public.whisper_nickname_versions n WHERE n.account_id=pa.id AND n.nickname_version=pv.nickname_version AND n.review_state='approved' AND n.revoked_at IS NULL AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x WHERE x.content_id=n.content_registry_id AND x.active)))
 AND (v.nickname_version IS NULL OR EXISTS(SELECT 1 FROM public.whisper_nickname_versions n WHERE n.account_id=p.author_id AND n.nickname_version=v.nickname_version AND n.review_state='approved' AND n.revoked_at IS NULL AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x WHERE x.content_id=n.content_registry_id AND x.active)))
 AND NOT EXISTS(SELECT 1 FROM public.content_restrictions x JOIN public.content_registry cr ON cr.id=x.content_id WHERE x.active AND cr.kind='whisper_post' AND cr.entity_id=parent.id)
 AND NOT EXISTS(SELECT 1 FROM public.privacy_holds h WHERE h.target_kind='whisper_post' AND h.target_id=parent.id AND h.status='active')
$$;
COMMENT ON FUNCTION public.whisper_public_report_body(source_kind text, source_id uuid) IS '举报所需当前公开正文最小投影，拒绝匿名身份及私密来源';

SET default_tablespace = '';

SET default_table_access_method = heap;

REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC;
