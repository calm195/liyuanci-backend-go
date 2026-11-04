-- 单应用账号只管理当前业务库；业务身份由会话、岗位及领域函数校验。
-- 不创建数据库角色、不授予集群管理权限，也不读取第二条连接。
DO $$
DECLARE item record;
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND
       (rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole OR rolreplication)) THEN
       RAISE EXCEPTION '应用账号不得拥有集群管理权限';
    END IF;
    -- 业务库对象必须归属于应用账号；不隐式转交或提权。
    IF EXISTS (SELECT 1 FROM pg_policies
       WHERE schemaname IN ('public','evidence') AND policyname <> 'application_account') THEN
       RAISE EXCEPTION '业务库存在未登记的行策略';
    END IF;
    FOR item IN SELECT n.nspname, c.relname FROM pg_class c
       JOIN pg_namespace n ON n.oid=c.relnamespace
       WHERE n.nspname IN ('public','evidence') AND c.relkind='r' LOOP
       IF NOT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
           JOIN pg_roles r ON r.oid=c.relowner
           WHERE n.nspname=item.nspname AND c.relname=item.relname AND r.rolname=current_user) THEN
           RAISE EXCEPTION '应用账号必须拥有当前业务库表';
       END IF;
       -- 保留RLS机制，只允许当前应用数据库账号；最终读写范围在领域函数按业务角色检查。
       EXECUTE format('DROP POLICY IF EXISTS application_account ON %I.%I',item.nspname,item.relname);
       EXECUTE format('CREATE POLICY application_account ON %I.%I TO %I USING (true) WITH CHECK (true)', item.nspname,item.relname,current_user);
       EXECUTE format('REVOKE ALL ON %I.%I FROM PUBLIC',item.nspname,item.relname);
    END LOOP;
END $$;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA evidence FROM PUBLIC;
REVOKE ALL ON SCHEMA evidence FROM PUBLIC;
