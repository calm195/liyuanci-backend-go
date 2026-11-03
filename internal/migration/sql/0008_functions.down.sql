-- 仅由迁移器在锁定并确认全部业务表为空后执行。
DROP FUNCTION public.clear_held_whisper_comment(bytea, uuid, jsonb);
DROP FUNCTION public.is_live_admin_session(bytea);
DROP FUNCTION public.is_live_student_session(bytea);
DROP FUNCTION public.lock_command_account(uuid);
DROP FUNCTION public.lock_command_admin(uuid);
DROP FUNCTION public.protect_interaction_block_policy();
DROP FUNCTION public.read_whisper_crisis_payload(bytea, uuid, uuid);
DROP FUNCTION public.read_whisper_review_payload(bytea, uuid);
DROP FUNCTION public.whisper_public_report_body(text, uuid);
