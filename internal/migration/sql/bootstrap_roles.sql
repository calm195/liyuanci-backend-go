-- 仅由专用数据库管理员显式执行；只建立无登录权限组，不创建账号或口令。
DO $$
DECLARE role_name text;
BEGIN
    FOREACH role_name IN ARRAY ARRAY['liyuanci_migrator', 'liyuanci_api', 'liyuanci_worker', 'liyuanci_identity_maintenance', 'liyuanci_admin_api', 'liyuanci_admin_maintenance', 'liyuanci_domain_worker', 'liyuanci_privacy_maintenance', 'liyuanci_evidence_reader', 'liyuanci_evidence_writer', 'liyuanci_evidence_purger', 'liyuanci_crisis_api'] LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', role_name);
        ELSIF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name AND
            (rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)) THEN
            RAISE EXCEPTION '权限组属性不安全，拒绝继续';
        END IF;
        IF EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members m JOIN pg_catalog.pg_roles r ON r.oid = m.member WHERE r.rolname = role_name) THEN
            RAISE EXCEPTION '权限组继承其他角色，拒绝继续';
        END IF;
    END LOOP;
END $$;

-- 数据库或 public schema 的所有者拥有不可通过普通 REVOKE 去除的权限，运行组不得占有它们。
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_catalog.pg_roles r
        WHERE r.rolname IN ('liyuanci_api', 'liyuanci_worker', 'liyuanci_identity_maintenance', 'liyuanci_admin_api', 'liyuanci_admin_maintenance', 'liyuanci_domain_worker', 'liyuanci_privacy_maintenance', 'liyuanci_evidence_reader', 'liyuanci_evidence_writer', 'liyuanci_evidence_purger', 'liyuanci_crisis_api')
        AND (r.oid = (SELECT datdba FROM pg_catalog.pg_database WHERE datname = current_database())
             OR r.oid = (SELECT nspowner FROM pg_catalog.pg_namespace WHERE nspname = 'public'))
    ) THEN
        RAISE EXCEPTION '运行权限组不得拥有目标数据库或公共命名空间';
    END IF;
    -- 未知策略可能是现场限制或历史放权；拒绝覆盖，不能静默删除或与它合并放宽边界。
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_policy WHERE polrelid = 'public.sessions'::regclass
        AND polname NOT IN ('sessions_student_scope', 'sessions_admin_scope', 'sessions_privacy_scope')) THEN
        RAISE EXCEPTION '会话存在未受管理策略，拒绝覆盖权限配置';
    END IF;
END $$;

-- 专用库显式矩阵；不使用未来所有表的默认宽授权。
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON SCHEMA public, evidence FROM liyuanci_api, liyuanci_worker, liyuanci_identity_maintenance, liyuanci_admin_api, liyuanci_admin_maintenance, liyuanci_domain_worker, liyuanci_privacy_maintenance, liyuanci_evidence_reader, liyuanci_evidence_writer, liyuanci_evidence_purger, liyuanci_crisis_api;
REVOKE ALL ON SCHEMA evidence FROM PUBLIC;
ALTER SCHEMA evidence OWNER TO liyuanci_migrator;
GRANT USAGE, CREATE ON SCHEMA public TO liyuanci_migrator;
-- 迁移凭据可建立后续受控 schema，运行组不获得此数据库级权限。
DO $$ BEGIN
    EXECUTE format('GRANT CREATE ON DATABASE %I TO liyuanci_migrator', current_database());
    EXECUTE format('REVOKE CREATE ON DATABASE %I FROM PUBLIC, liyuanci_api, liyuanci_worker, liyuanci_identity_maintenance, liyuanci_admin_api, liyuanci_admin_maintenance, liyuanci_domain_worker, liyuanci_privacy_maintenance, liyuanci_evidence_reader, liyuanci_evidence_writer, liyuanci_evidence_purger, liyuanci_crisis_api', current_database());
END $$;
GRANT USAGE ON SCHEMA public TO liyuanci_api, liyuanci_worker, liyuanci_identity_maintenance, liyuanci_admin_api, liyuanci_admin_maintenance, liyuanci_domain_worker, liyuanci_privacy_maintenance;
GRANT USAGE ON SCHEMA evidence TO liyuanci_evidence_reader, liyuanci_evidence_writer, liyuanci_evidence_purger;
ALTER TABLE public.schema_migrations OWNER TO liyuanci_migrator;
ALTER TABLE public.accounts OWNER TO liyuanci_migrator;
ALTER TABLE public.identity_lookup_keys OWNER TO liyuanci_migrator;
ALTER TABLE public.identity_secrets OWNER TO liyuanci_migrator;
ALTER TABLE public.identity_lookup_claims OWNER TO liyuanci_migrator;
ALTER TABLE public.identity_names OWNER TO liyuanci_migrator;
ALTER TABLE public.admin_accounts OWNER TO liyuanci_migrator;
ALTER TABLE public.admin_role_bindings OWNER TO liyuanci_migrator;
ALTER TABLE public.sessions OWNER TO liyuanci_migrator;
ALTER TABLE public.assets OWNER TO liyuanci_migrator;
ALTER TABLE public.identity_reviews OWNER TO liyuanci_migrator;
ALTER TABLE public.blocks OWNER TO liyuanci_migrator;
ALTER TABLE public.penalties OWNER TO liyuanci_migrator;
ALTER TABLE public.appeals OWNER TO liyuanci_migrator;
ALTER TABLE public.wishes OWNER TO liyuanci_migrator;
ALTER TABLE public.wish_versions OWNER TO liyuanci_migrator;
ALTER TABLE public.applications OWNER TO liyuanci_migrator;
ALTER TABLE public.matches OWNER TO liyuanci_migrator;
ALTER TABLE public.conversations OWNER TO liyuanci_migrator;
ALTER TABLE public.messages OWNER TO liyuanci_migrator;
ALTER TABLE public.story_versions OWNER TO liyuanci_migrator;
ALTER TABLE public.content_registry OWNER TO liyuanci_migrator;
ALTER TABLE public.wordlist_versions OWNER TO liyuanci_migrator;
ALTER TABLE public.wordlist_entries OWNER TO liyuanci_migrator;
ALTER TABLE public.moderation_tasks OWNER TO liyuanci_migrator;
ALTER TABLE public.reports OWNER TO liyuanci_migrator;
ALTER TABLE public.content_restrictions OWNER TO liyuanci_migrator;
ALTER TABLE public.privacy_holds OWNER TO liyuanci_migrator;
ALTER TABLE public.work_items OWNER TO liyuanci_migrator;
ALTER TABLE public.outbox_events OWNER TO liyuanci_migrator;
ALTER TABLE public.consumer_receipts OWNER TO liyuanci_migrator;
ALTER TABLE public.notifications OWNER TO liyuanci_migrator;
ALTER TABLE public.delivery_attempts OWNER TO liyuanci_migrator;
ALTER TABLE public.command_receipts OWNER TO liyuanci_migrator;
ALTER TABLE public.access_audits OWNER TO liyuanci_migrator;
ALTER TABLE public.deletion_jobs OWNER TO liyuanci_migrator;
ALTER TABLE evidence.evidence_cases OWNER TO liyuanci_migrator;
ALTER TABLE evidence.crisis_cases OWNER TO liyuanci_migrator;
ALTER TABLE evidence.assets OWNER TO liyuanci_migrator;
ALTER TABLE evidence.evidence_items OWNER TO liyuanci_migrator;
ALTER TABLE evidence.evidence_actions OWNER TO liyuanci_migrator;
REVOKE ALL ON public.schema_migrations, public.accounts, public.identity_lookup_keys, public.identity_secrets, public.identity_lookup_claims, public.identity_names, public.admin_accounts, public.admin_role_bindings, public.sessions, public.assets, public.identity_reviews, public.blocks, public.penalties, public.appeals, public.wishes, public.wish_versions, public.applications, public.matches, public.conversations, public.messages, public.story_versions, public.content_registry, public.wordlist_versions, public.wordlist_entries, public.moderation_tasks, public.reports, public.content_restrictions, public.privacy_holds, public.work_items, public.outbox_events, public.consumer_receipts, public.notifications, public.delivery_attempts, public.command_receipts, public.access_audits, public.deletion_jobs, evidence.evidence_cases, evidence.crisis_cases, evidence.assets, evidence.evidence_items, evidence.evidence_actions FROM PUBLIC, liyuanci_api, liyuanci_worker, liyuanci_identity_maintenance, liyuanci_admin_api, liyuanci_admin_maintenance, liyuanci_domain_worker, liyuanci_privacy_maintenance, liyuanci_evidence_reader, liyuanci_evidence_writer, liyuanci_evidence_purger, liyuanci_crisis_api;
-- 表级 REVOKE 不清除历史列级 ACL；显式清理回执列，防止重放后仍可改主体或幂等键。
REVOKE ALL (actor_kind, actor_id, command_kind, key_hash, request_hash, result_ref, expires_at)
ON public.command_receipts FROM PUBLIC, liyuanci_api, liyuanci_worker, liyuanci_identity_maintenance, liyuanci_admin_api, liyuanci_admin_maintenance, liyuanci_domain_worker, liyuanci_privacy_maintenance, liyuanci_evidence_reader, liyuanci_evidence_writer, liyuanci_evidence_purger, liyuanci_crisis_api;
GRANT SELECT, INSERT, UPDATE ON public.accounts, public.sessions, public.identity_names, public.wishes, public.wish_versions, public.applications, public.matches, public.conversations, public.messages, public.story_versions, public.content_registry, public.blocks, public.reports, public.appeals, public.notifications, public.deletion_jobs TO liyuanci_api;
GRANT SELECT ON public.identity_lookup_keys, public.wordlist_versions, public.wordlist_entries, public.penalties, public.identity_reviews, public.content_restrictions, public.privacy_holds, public.assets TO liyuanci_api;
GRANT SELECT, INSERT ON public.identity_secrets, public.identity_lookup_claims, public.identity_reviews, public.assets, public.moderation_tasks, public.work_items, public.outbox_events, public.command_receipts TO liyuanci_api;
GRANT SELECT ON public.accounts TO liyuanci_worker;
GRANT SELECT ON public.accounts TO liyuanci_identity_maintenance;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.identity_lookup_keys, public.identity_secrets, public.identity_lookup_claims, public.identity_names TO liyuanci_identity_maintenance;
GRANT SELECT ON public.admin_accounts, public.admin_role_bindings, public.accounts, public.identity_names, public.identity_secrets, public.identity_reviews, public.assets, public.content_registry, public.wish_versions, public.applications, public.messages, public.story_versions, public.reports, public.appeals, public.penalties TO liyuanci_admin_api;
GRANT SELECT, INSERT, UPDATE ON public.sessions, public.moderation_tasks, public.wordlist_versions, public.wordlist_entries, public.penalties, public.reports, public.appeals, public.content_restrictions, public.access_audits, public.work_items, public.outbox_events TO liyuanci_admin_api;
GRANT SELECT, INSERT ON public.command_receipts TO liyuanci_admin_api;
GRANT UPDATE ON public.identity_reviews, public.accounts TO liyuanci_admin_api;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.admin_accounts, public.admin_role_bindings TO liyuanci_admin_maintenance;
GRANT SELECT, UPDATE ON public.sessions TO liyuanci_admin_maintenance;
GRANT SELECT ON public.accounts, public.wishes, public.wish_versions, public.applications, public.matches, public.conversations, public.messages, public.story_versions, public.content_registry, public.wordlist_versions, public.wordlist_entries, public.content_restrictions, public.privacy_holds TO liyuanci_domain_worker;
GRANT SELECT, INSERT, UPDATE ON public.work_items, public.outbox_events, public.moderation_tasks, public.notifications, public.delivery_attempts, public.consumer_receipts TO liyuanci_domain_worker;
GRANT UPDATE ON public.wishes, public.wish_versions, public.applications, public.matches, public.conversations, public.messages, public.story_versions, public.content_registry TO liyuanci_domain_worker;
GRANT SELECT, UPDATE, DELETE ON public.accounts, public.identity_secrets, public.identity_lookup_claims, public.identity_names, public.sessions, public.assets, public.identity_reviews, public.blocks, public.penalties, public.appeals, public.wishes, public.wish_versions, public.applications, public.matches, public.conversations, public.messages, public.story_versions, public.content_registry, public.moderation_tasks, public.reports, public.content_restrictions, public.privacy_holds, public.work_items, public.outbox_events, public.consumer_receipts, public.notifications, public.delivery_attempts, public.command_receipts, public.access_audits, public.deletion_jobs TO liyuanci_privacy_maintenance;
GRANT INSERT ON public.privacy_holds, public.work_items, public.outbox_events TO liyuanci_privacy_maintenance;
GRANT SELECT ON evidence.evidence_cases, evidence.crisis_cases, evidence.assets, evidence.evidence_items, evidence.evidence_actions TO liyuanci_evidence_reader;
GRANT SELECT, INSERT, UPDATE ON evidence.evidence_cases, evidence.crisis_cases, evidence.assets, evidence.evidence_items TO liyuanci_evidence_writer;
GRANT SELECT, INSERT ON evidence.evidence_actions TO liyuanci_evidence_writer;
GRANT SELECT, UPDATE, DELETE ON evidence.evidence_cases, evidence.crisis_cases, evidence.assets, evidence.evidence_items, evidence.evidence_actions TO liyuanci_evidence_purger;

-- 命名空间隔离在数据库内生效；同一命名空间中的本人/案件授权仍由服务复核。
ALTER TABLE public.sessions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS sessions_student_scope ON public.sessions;
CREATE POLICY sessions_student_scope ON public.sessions FOR ALL TO liyuanci_api, liyuanci_identity_maintenance
    USING (account_id IS NOT NULL) WITH CHECK (account_id IS NOT NULL);
DROP POLICY IF EXISTS sessions_admin_scope ON public.sessions;
CREATE POLICY sessions_admin_scope ON public.sessions FOR ALL TO liyuanci_admin_api, liyuanci_admin_maintenance, liyuanci_crisis_api
    USING (admin_id IS NOT NULL) WITH CHECK (admin_id IS NOT NULL);
DROP POLICY IF EXISTS sessions_privacy_scope ON public.sessions;
CREATE POLICY sessions_privacy_scope ON public.sessions FOR ALL TO liyuanci_privacy_maintenance
    USING (account_id IS NOT NULL) WITH CHECK (account_id IS NOT NULL);

-- 命令入口在同一主体锁内重用过期回执，不删除未过期结果，更新权限仅限回执字段。
GRANT UPDATE (request_hash, result_ref, expires_at) ON public.command_receipts TO liyuanci_api, liyuanci_admin_api;

-- 后台 API 需要锁定当前授权主体，但不应获得修改后台身份表的权限。
CREATE OR REPLACE FUNCTION public.lock_command_admin(subject uuid)
RETURNS TABLE(id uuid, status text, auth_epoch bigint)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog
AS $$ SELECT a.id, a.status, a.auth_epoch FROM public.admin_accounts a WHERE a.id = subject FOR UPDATE $$;
ALTER FUNCTION public.lock_command_admin(uuid) OWNER TO liyuanci_migrator;
COMMENT ON FUNCTION public.lock_command_admin(uuid) IS '仅锁定并返回后台授权 guard，锁随调用事务结束，不提供身份修改能力';
REVOKE ALL ON FUNCTION public.lock_command_admin(uuid) FROM PUBLIC, liyuanci_api, liyuanci_worker, liyuanci_identity_maintenance, liyuanci_admin_api, liyuanci_admin_maintenance, liyuanci_domain_worker, liyuanci_privacy_maintenance, liyuanci_evidence_reader, liyuanci_evidence_writer, liyuanci_evidence_purger, liyuanci_crisis_api;
GRANT EXECUTE ON FUNCTION public.lock_command_admin(uuid) TO liyuanci_admin_api, liyuanci_admin_maintenance;

-- 领域消费者需要串行复核账号资格，但不能因此获得修改账号的宽权限。
CREATE OR REPLACE FUNCTION public.lock_command_account(subject uuid)
RETURNS TABLE(id uuid, lifecycle text, verification text, auth_epoch bigint, version bigint)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog
AS $$ SELECT a.id, a.lifecycle, a.verification, a.auth_epoch, a.version FROM public.accounts a WHERE a.id = subject FOR UPDATE $$;
ALTER FUNCTION public.lock_command_account(uuid) OWNER TO liyuanci_migrator;
COMMENT ON FUNCTION public.lock_command_account(uuid) IS '锁定账号 guard 并返回当前授权元数据，不提供账号修改能力';
REVOKE ALL ON FUNCTION public.lock_command_account(uuid) FROM PUBLIC, liyuanci_api, liyuanci_worker, liyuanci_identity_maintenance, liyuanci_admin_api, liyuanci_admin_maintenance, liyuanci_domain_worker, liyuanci_privacy_maintenance, liyuanci_evidence_reader, liyuanci_evidence_writer, liyuanci_evidence_purger, liyuanci_crisis_api;
GRANT EXECUTE ON FUNCTION public.lock_command_account(uuid) TO liyuanci_api, liyuanci_admin_api, liyuanci_domain_worker, liyuanci_privacy_maintenance;

-- 后台重驱动仅检查/追加去重标识，不能改写既有回执或读取无关完成时间。
REVOKE ALL (consumer_kind, operation_id, completed_at) ON public.consumer_receipts FROM PUBLIC, liyuanci_admin_api;
GRANT SELECT (consumer_kind, operation_id), INSERT (consumer_kind, operation_id) ON public.consumer_receipts TO liyuanci_admin_api;

-- 独立后台身份连接只复用维护组；业务后台API不因此获得口令或身份写入权限。
ALTER TABLE public.admin_account_actions OWNER TO liyuanci_migrator;
REVOKE ALL ON public.admin_account_actions FROM PUBLIC, liyuanci_api, liyuanci_worker, liyuanci_identity_maintenance, liyuanci_admin_api, liyuanci_admin_maintenance, liyuanci_domain_worker, liyuanci_privacy_maintenance, liyuanci_evidence_reader, liyuanci_evidence_writer, liyuanci_evidence_purger, liyuanci_crisis_api;
GRANT SELECT, INSERT ON public.admin_account_actions TO liyuanci_admin_maintenance;
GRANT INSERT ON public.sessions TO liyuanci_admin_maintenance;
GRANT SELECT, INSERT ON public.command_receipts TO liyuanci_admin_maintenance;
GRANT UPDATE (request_hash, result_ref, expires_at) ON public.command_receipts TO liyuanci_admin_maintenance;

-- 学生身份装配使用专用身份维护组，不向普通API或后台身份池开放微信占用/会话写入。
ALTER TABLE public.student_login_exchanges OWNER TO liyuanci_migrator;
REVOKE ALL ON public.student_login_exchanges FROM PUBLIC, liyuanci_api, liyuanci_worker, liyuanci_identity_maintenance, liyuanci_admin_api, liyuanci_admin_maintenance, liyuanci_domain_worker, liyuanci_privacy_maintenance, liyuanci_evidence_reader, liyuanci_evidence_writer, liyuanci_evidence_purger, liyuanci_crisis_api;
GRANT SELECT, INSERT, UPDATE ON public.student_login_exchanges TO liyuanci_identity_maintenance;
GRANT SELECT, INSERT ON public.sessions TO liyuanci_identity_maintenance;
GRANT UPDATE (revoked_at) ON public.sessions TO liyuanci_identity_maintenance;
GRANT INSERT ON public.accounts TO liyuanci_identity_maintenance;
GRANT UPDATE (nickname, hide_identity_tags, version) ON public.accounts TO liyuanci_identity_maintenance;
GRANT SELECT, INSERT ON public.command_receipts TO liyuanci_identity_maintenance;
GRANT UPDATE (request_hash, result_ref, expires_at) ON public.command_receipts TO liyuanci_identity_maintenance;
GRANT EXECUTE ON FUNCTION public.lock_command_account(uuid) TO liyuanci_identity_maintenance;
GRANT SELECT ON public.penalties TO liyuanci_identity_maintenance;

-- 学生卡暂存与外部处理分配给独立领域身份；普通后台列表不能读取图片密文。
ALTER TABLE public.student_card_payloads OWNER TO liyuanci_migrator;
REVOKE ALL ON public.student_card_payloads FROM PUBLIC,liyuanci_api,liyuanci_worker,liyuanci_identity_maintenance,liyuanci_admin_api,liyuanci_admin_maintenance,liyuanci_domain_worker,liyuanci_privacy_maintenance,liyuanci_evidence_reader,liyuanci_evidence_writer,liyuanci_evidence_purger, liyuanci_crisis_api;
GRANT SELECT,INSERT ON public.assets,public.student_card_payloads TO liyuanci_identity_maintenance;
GRANT SELECT,INSERT ON public.work_items,public.outbox_events TO liyuanci_identity_maintenance;
GRANT SELECT,UPDATE ON public.assets TO liyuanci_domain_worker;
GRANT SELECT,DELETE ON public.student_card_payloads TO liyuanci_domain_worker;
GRANT SELECT,DELETE ON public.student_card_payloads TO liyuanci_privacy_maintenance;

-- 人工认证按用途隔离数据库池；审核通过时只授予必要的加密身份绑定权限。
GRANT SELECT, INSERT, UPDATE ON public.identity_reviews TO liyuanci_identity_maintenance;
GRANT UPDATE (verification) ON public.accounts TO liyuanci_identity_maintenance;
GRANT SELECT ON public.identity_lookup_keys, public.identity_lookup_claims TO liyuanci_admin_api;
GRANT INSERT ON public.identity_lookup_claims, public.identity_secrets, public.identity_names TO liyuanci_admin_api;
-- PostgreSQL行锁要求至少一列UPDATE权限；只授予版本列，不允许审核入口改写对象键或扫描结果。
GRANT UPDATE (version) ON public.assets TO liyuanci_identity_maintenance, liyuanci_admin_api;

-- 心愿命令只使旧审核/工作失效，不允许学生接口写入人工结论或删除历史审核记录。
GRANT UPDATE (status,version) ON public.moderation_tasks TO liyuanci_api;
GRANT UPDATE (status, lease_until, version) ON public.work_items TO liyuanci_api;

-- 词库与扫描快照采用独立窄权限；普通API不读取待放行脱敏候选。
ALTER TABLE public.wordlist_heads OWNER TO liyuanci_migrator;
ALTER TABLE public.moderation_scans OWNER TO liyuanci_migrator;
REVOKE ALL ON public.wordlist_heads,public.moderation_scans FROM PUBLIC,liyuanci_api,liyuanci_worker,liyuanci_identity_maintenance,liyuanci_admin_api,liyuanci_admin_maintenance,liyuanci_domain_worker,liyuanci_privacy_maintenance,liyuanci_evidence_reader,liyuanci_evidence_writer,liyuanci_evidence_purger, liyuanci_crisis_api;
GRANT SELECT,UPDATE ON public.wordlist_heads TO liyuanci_admin_api;
GRANT SELECT ON public.wordlist_heads TO liyuanci_domain_worker;
GRANT SELECT ON public.moderation_scans TO liyuanci_admin_api;
GRANT SELECT,INSERT ON public.moderation_scans TO liyuanci_domain_worker;
GRANT SELECT,DELETE ON public.moderation_scans TO liyuanci_privacy_maintenance;
GRANT INSERT ON public.content_restrictions TO liyuanci_domain_worker;
GRANT SELECT ON public.wishes,public.privacy_holds TO liyuanci_admin_api;
GRANT UPDATE (state,version) ON public.wishes TO liyuanci_admin_api;
GRANT UPDATE (moderation_status,public_body,identity_tags,school_related,school_detection_version,school_detection_state,wordlist_version) ON public.wish_versions TO liyuanci_admin_api;
GRANT UPDATE (status,school_related,school_detection_version,school_detection_state) ON public.content_registry TO liyuanci_admin_api;

-- 草案和归档内容不提供编辑能力，状态转换只允许资源版本及生命周期列。
REVOKE UPDATE,DELETE ON public.wordlist_entries FROM liyuanci_admin_api;
REVOKE UPDATE ON public.wordlist_versions FROM liyuanci_admin_api;
GRANT UPDATE (status,version) ON public.wordlist_versions TO liyuanci_admin_api;

-- 最终扫描复核读取当前处罚元数据，领域工作组仍没有处罚写入权。
GRANT SELECT ON public.penalties TO liyuanci_domain_worker;

-- 应愿审核与匹配复核只读双向拉黑，不授予领域消费者操作黑名单的权限。
GRANT SELECT ON public.blocks TO liyuanci_domain_worker,liyuanci_admin_api;
GRANT UPDATE (status,version,public_message_cipher) ON public.applications TO liyuanci_admin_api;
GRANT SELECT,INSERT ON public.notifications TO liyuanci_admin_api;
GRANT SELECT ON public.matches TO liyuanci_admin_api;

-- 私聊终审沿用统一锁序，只授予送达所需字段写权限。
GRANT SELECT ON public.conversations TO liyuanci_admin_api;
GRANT UPDATE (next_delivery_seq,version) ON public.conversations TO liyuanci_admin_api;
GRANT UPDATE (status,version,delivery_seq,delivered_at) ON public.messages TO liyuanci_admin_api;
GRANT UPDATE (version) ON public.matches TO liyuanci_admin_api;

-- 分享只持有技术引用，不授予运行身份DDL或历史正文快照能力。
ALTER TABLE public.share_links OWNER TO liyuanci_migrator;
REVOKE ALL ON public.share_links FROM PUBLIC,liyuanci_api,liyuanci_domain_worker,liyuanci_admin_api,liyuanci_privacy_maintenance;
GRANT SELECT,INSERT ON public.share_links TO liyuanci_api;
GRANT SELECT,DELETE ON public.share_links TO liyuanci_privacy_maintenance;
GRANT UPDATE (current_story_version) ON public.wishes TO liyuanci_admin_api;
GRANT UPDATE (moderation_status,public_body,identity_tags,school_related,school_detection_version,school_detection_state,wordlist_version) ON public.story_versions TO liyuanci_admin_api;
GRANT UPDATE (status,released_at) ON public.privacy_holds TO liyuanci_admin_api,liyuanci_domain_worker;

-- 安全治理仍区分学生可确认警告列和后台可写处罚列，不开放DDL或任意处罚写权限。
ALTER TABLE public.report_types OWNER TO liyuanci_migrator;
REVOKE ALL ON public.report_types FROM PUBLIC,liyuanci_api,liyuanci_worker,liyuanci_identity_maintenance,liyuanci_admin_api,liyuanci_admin_maintenance,liyuanci_domain_worker,liyuanci_privacy_maintenance;
GRANT SELECT ON public.report_types TO liyuanci_api,liyuanci_admin_api;
GRANT DELETE ON public.blocks TO liyuanci_api;
GRANT UPDATE (first_displayed_at,confirmable_at,acknowledged_at,version) ON public.penalties TO liyuanci_api;
GRANT UPDATE (restriction_epoch) ON public.wishes TO liyuanci_admin_api;
GRANT UPDATE (write_epoch) ON public.conversations TO liyuanci_admin_api;
GRANT UPDATE (cancel_reason) ON public.applications TO liyuanci_admin_api;

-- 内容危机命中必须在原领域事务内保存限定证据；只授指定证据表，不授DDL或清除权限。
GRANT USAGE ON SCHEMA evidence TO liyuanci_crisis_api,liyuanci_domain_worker;
GRANT SELECT,INSERT ON evidence.evidence_cases,evidence.evidence_items,evidence.evidence_actions TO liyuanci_crisis_api,liyuanci_domain_worker;
GRANT SELECT,INSERT,UPDATE ON evidence.crisis_cases,evidence.crisis_deliveries TO liyuanci_crisis_api,liyuanci_domain_worker;
GRANT SELECT ON public.identity_secrets TO liyuanci_domain_worker;
ALTER TABLE evidence.crisis_deliveries OWNER TO liyuanci_migrator;
ALTER TABLE public.care_cards OWNER TO liyuanci_migrator;
REVOKE ALL ON evidence.crisis_deliveries,public.care_cards FROM PUBLIC;
GRANT SELECT,INSERT ON public.care_cards TO liyuanci_domain_worker;
GRANT SELECT ON public.care_cards TO liyuanci_api;
GRANT SELECT,UPDATE,DELETE ON public.care_cards TO liyuanci_privacy_maintenance;
GRANT SELECT ON evidence.crisis_deliveries TO liyuanci_evidence_reader;
GRANT SELECT,INSERT,UPDATE ON evidence.crisis_deliveries TO liyuanci_evidence_writer;
GRANT SELECT,UPDATE,DELETE ON evidence.crisis_deliveries TO liyuanci_evidence_purger;

-- 危机运行组使用显式最小后台权限，不继承普通审核组，不写身份、词库、处罚或证据删除。
GRANT USAGE ON SCHEMA public TO liyuanci_crisis_api;
GRANT SELECT ON public.admin_accounts,public.admin_role_bindings,public.accounts,public.identity_secrets,public.sessions,public.wishes,public.wish_versions,public.story_versions,public.applications,public.messages,public.matches,public.conversations,public.moderation_tasks,public.moderation_scans,public.content_registry,public.reports,public.penalties,public.blocks,public.privacy_holds TO liyuanci_crisis_api;
GRANT UPDATE (id) ON public.sessions TO liyuanci_crisis_api;
GRANT EXECUTE ON FUNCTION public.lock_command_admin(uuid),public.lock_command_account(uuid) TO liyuanci_crisis_api;
GRANT SELECT,INSERT ON public.command_receipts TO liyuanci_crisis_api;
GRANT UPDATE (request_hash,result_ref,expires_at) ON public.command_receipts TO liyuanci_crisis_api;
GRANT INSERT,UPDATE ON public.moderation_tasks TO liyuanci_crisis_api;
GRANT INSERT ON public.moderation_scans TO liyuanci_crisis_api;
GRANT SELECT,INSERT,UPDATE ON public.content_restrictions TO liyuanci_crisis_api;
GRANT UPDATE (state,version,restriction_epoch) ON public.wishes TO liyuanci_crisis_api;
GRANT UPDATE (review_task_id,moderation_status,public_body) ON public.wish_versions,public.story_versions TO liyuanci_crisis_api;
GRANT UPDATE (review_task_id,status,version,public_message_cipher) ON public.applications TO liyuanci_crisis_api;
GRANT UPDATE (review_task_id,status,version) ON public.messages TO liyuanci_crisis_api;
GRANT UPDATE (version) ON public.conversations,public.matches TO liyuanci_crisis_api;
GRANT UPDATE (status) ON public.content_registry TO liyuanci_crisis_api;
GRANT UPDATE (status,handler_id,version) ON public.reports TO liyuanci_crisis_api;
