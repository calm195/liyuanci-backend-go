-- 普通后台只能读取当前普通命中案件的单份正文密文，不能扫描胶囊或身份原稿。
CREATE FUNCTION public.read_whisper_review_payload(token_digest bytea, task_ref uuid)
RETURNS TABLE(body jsonb)
LANGUAGE sql SECURITY DEFINER STABLE SET search_path=pg_catalog,public AS $$
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
COMMENT ON FUNCTION public.read_whisper_review_payload(bytea,uuid) IS '当前普通命中树洞案件的单份原稿授权投影；不返回身份或内部账号';
REVOKE ALL ON FUNCTION public.read_whisper_review_payload(bytea,uuid) FROM PUBLIC;

CREATE FUNCTION public.whisper_public_report_body(source_kind text,source_id uuid)
RETURNS TABLE(body text)
LANGUAGE sql SECURITY DEFINER STABLE SET search_path=pg_catalog,public AS $$
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
COMMENT ON FUNCTION public.whisper_public_report_body(text,uuid) IS '举报所需当前公开正文最小投影，拒绝匿名身份及私密来源';
REVOKE ALL ON FUNCTION public.whisper_public_report_body(text,uuid) FROM PUBLIC;

ALTER TABLE evidence.crisis_cases ADD COLUMN source_entity_id uuid;
ALTER TABLE evidence.crisis_cases ADD COLUMN source_content_version bigint;
UPDATE evidence.crisis_cases cc SET source_entity_id=cr.entity_id,source_content_version=cr.content_version FROM evidence.evidence_cases ec JOIN public.content_registry cr ON cr.id=ec.source_ref WHERE cc.evidence_case_id=ec.id AND cc.content_type IN ('whisper_post','whisper_comment','whisper_nickname','whisper_capsule');
ALTER TABLE evidence.crisis_cases ADD CONSTRAINT crisis_whisper_source CHECK ((content_type NOT IN ('whisper_post','whisper_comment','whisper_nickname','whisper_capsule')) OR (source_entity_id IS NOT NULL AND source_content_version IS NOT NULL AND source_content_version>0));
COMMENT ON COLUMN evidence.crisis_cases.source_entity_id IS '树洞案件的安全来源随机标识，普通数据删除后仍可限定投影';
COMMENT ON COLUMN evidence.crisis_cases.source_content_version IS '树洞案件精确正文版本，不随普通数据删除或换版变化';

CREATE FUNCTION public.read_whisper_crisis_payload(token_digest bytea,task_ref uuid,report_ref uuid)
RETURNS TABLE(body jsonb)
LANGUAGE sql SECURITY DEFINER STABLE SET search_path=pg_catalog,public AS $$
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
COMMENT ON FUNCTION public.read_whisper_crisis_payload(bytea,uuid,uuid) IS '有权危机升格来源的精确原稿投影；普通案件升格仅限同时具备两域权限的超管';
REVOKE ALL ON FUNCTION public.read_whisper_crisis_payload(bytea,uuid,uuid) FROM PUBLIC;

ALTER TABLE public.deletion_jobs ADD COLUMN source_module text NOT NULL DEFAULT 'legacy' CHECK(source_module IN ('legacy','whispers'));
COMMENT ON COLUMN public.deletion_jobs.source_module IS '删除回执投影命名空间，普通和树洞后台不得跨域读取';
