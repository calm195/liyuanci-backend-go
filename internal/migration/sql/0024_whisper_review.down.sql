DROP FUNCTION public.read_whisper_review_payload(bytea,uuid);
DROP FUNCTION public.whisper_public_report_body(text,uuid);
ALTER TABLE evidence.crisis_cases DROP CONSTRAINT crisis_whisper_source;
ALTER TABLE evidence.crisis_cases DROP COLUMN source_content_version;
ALTER TABLE evidence.crisis_cases DROP COLUMN source_entity_id;
DROP FUNCTION public.read_whisper_crisis_payload(bytea,uuid,uuid);
ALTER TABLE public.deletion_jobs DROP COLUMN source_module;
