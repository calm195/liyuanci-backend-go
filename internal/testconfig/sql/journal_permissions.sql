-- 不建立登录身份，也不向业务角色授予恢复日志权限。
DO $$
DECLARE role_name text;
BEGIN
    FOREACH role_name IN ARRAY ARRAY['liyuanci_recovery_migrator', 'liyuanci_recovery_writer', 'liyuanci_recovery_reader', 'liyuanci_recovery_retention'] LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', role_name);
        ELSIF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name AND
            (rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)) THEN
            RAISE EXCEPTION '恢复权限组属性不安全';
        END IF;
        IF EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members m JOIN pg_catalog.pg_roles r ON r.oid = m.member WHERE r.rolname = role_name) THEN
            RAISE EXCEPTION '恢复权限组存在额外继承';
        END IF;
    END LOOP;
END $$;
-- 所有者可移除整个命名空间，不能把这种身份当作受限恢复日志读写角色。
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_catalog.pg_roles r
        WHERE r.rolname IN ('liyuanci_recovery_writer', 'liyuanci_recovery_reader', 'liyuanci_recovery_retention')
        AND (r.oid = (SELECT datdba FROM pg_catalog.pg_database WHERE datname = current_database())
             OR r.oid = (SELECT nspowner FROM pg_catalog.pg_namespace WHERE nspname = 'public'))
    ) THEN
        RAISE EXCEPTION '恢复运行组不得拥有数据库或公共命名空间';
    END IF;
END $$;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
DO $$ BEGIN
    EXECUTE format('REVOKE CREATE ON DATABASE %I FROM PUBLIC, liyuanci_recovery_writer, liyuanci_recovery_reader, liyuanci_recovery_retention', current_database());
END $$;
REVOKE ALL ON SCHEMA public FROM liyuanci_recovery_writer, liyuanci_recovery_reader, liyuanci_recovery_retention;
GRANT USAGE, CREATE ON SCHEMA public TO liyuanci_recovery_migrator;
GRANT USAGE ON SCHEMA public TO liyuanci_recovery_writer, liyuanci_recovery_reader, liyuanci_recovery_retention;
ALTER TABLE public.recovery_schema_migrations OWNER TO liyuanci_recovery_migrator;
ALTER TABLE public.recovery_delete_journal OWNER TO liyuanci_recovery_migrator;
REVOKE ALL ON public.recovery_schema_migrations, public.recovery_delete_journal FROM PUBLIC, liyuanci_recovery_writer, liyuanci_recovery_reader, liyuanci_recovery_retention;
REVOKE ALL ON SEQUENCE public.recovery_delete_journal_record_no_seq FROM PUBLIC, liyuanci_recovery_writer, liyuanci_recovery_reader, liyuanci_recovery_retention;
-- 若同集群中曾误授业务组权限，显式撤销；独立服务器不存在这些组时无需创建它们。
DO $$
DECLARE role_name text;
BEGIN
    FOREACH role_name IN ARRAY ARRAY['liyuanci_migrator', 'liyuanci_api', 'liyuanci_worker', 'liyuanci_identity_maintenance', 'liyuanci_admin_api', 'liyuanci_admin_maintenance', 'liyuanci_domain_worker', 'liyuanci_privacy_maintenance', 'liyuanci_evidence_reader', 'liyuanci_evidence_writer', 'liyuanci_evidence_purger'] LOOP
        IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('REVOKE ALL ON public.recovery_schema_migrations, public.recovery_delete_journal FROM %I', role_name);
            EXECUTE format('REVOKE ALL ON SEQUENCE public.recovery_delete_journal_record_no_seq FROM %I', role_name);
        END IF;
    END LOOP;
END $$;
GRANT INSERT, SELECT ON public.recovery_delete_journal TO liyuanci_recovery_writer;
GRANT UPDATE (confirmed_at) ON public.recovery_delete_journal TO liyuanci_recovery_writer;
GRANT USAGE ON SEQUENCE public.recovery_delete_journal_record_no_seq TO liyuanci_recovery_writer;
GRANT SELECT ON public.recovery_delete_journal TO liyuanci_recovery_reader;
GRANT SELECT, DELETE ON public.recovery_delete_journal TO liyuanci_recovery_retention;
