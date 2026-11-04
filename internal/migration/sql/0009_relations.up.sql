-- 跨表外键和单向切换触发器；版本 SQL 是结构定义的唯一来源。

CREATE TRIGGER interaction_block_policy_monotonic BEFORE DELETE OR UPDATE ON public.interaction_block_policy FOR EACH ROW EXECUTE FUNCTION public.protect_interaction_block_policy();

ALTER TABLE ONLY evidence.assets
    ADD CONSTRAINT assets_case_id_fkey FOREIGN KEY (case_id) REFERENCES evidence.evidence_cases(id);

ALTER TABLE ONLY evidence.crisis_cases
    ADD CONSTRAINT crisis_cases_evidence_case_id_fkey FOREIGN KEY (evidence_case_id) REFERENCES evidence.evidence_cases(id);

ALTER TABLE ONLY evidence.crisis_deliveries
    ADD CONSTRAINT crisis_deliveries_case_id_fkey FOREIGN KEY (case_id) REFERENCES evidence.crisis_cases(id);

ALTER TABLE ONLY evidence.evidence_actions
    ADD CONSTRAINT evidence_actions_case_id_fkey FOREIGN KEY (case_id) REFERENCES evidence.evidence_cases(id);

ALTER TABLE ONLY evidence.evidence_items
    ADD CONSTRAINT evidence_items_asset_ref_case_id_fkey FOREIGN KEY (asset_ref, case_id) REFERENCES evidence.assets(id, case_id);

ALTER TABLE ONLY evidence.evidence_items
    ADD CONSTRAINT evidence_items_case_id_fkey FOREIGN KEY (case_id) REFERENCES evidence.evidence_cases(id);

ALTER TABLE ONLY public.access_audits
    ADD CONSTRAINT access_audits_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.admin_accounts(id);

ALTER TABLE ONLY public.admin_account_actions
    ADD CONSTRAINT admin_account_actions_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.admin_accounts(id);

ALTER TABLE ONLY public.admin_account_actions
    ADD CONSTRAINT admin_account_actions_target_id_fkey FOREIGN KEY (target_id) REFERENCES public.admin_accounts(id);

ALTER TABLE ONLY public.admin_role_bindings
    ADD CONSTRAINT admin_role_bindings_admin_id_fkey FOREIGN KEY (admin_id) REFERENCES public.admin_accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.appeals
    ADD CONSTRAINT appeals_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.appeals
    ADD CONSTRAINT appeals_handler_id_fkey FOREIGN KEY (handler_id) REFERENCES public.admin_accounts(id);

ALTER TABLE ONLY public.appeals
    ADD CONSTRAINT appeals_penalty_id_fkey FOREIGN KEY (penalty_id) REFERENCES public.penalties(id);

ALTER TABLE ONLY public.applications
    ADD CONSTRAINT applications_applicant_id_fkey FOREIGN KEY (applicant_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.applications
    ADD CONSTRAINT applications_review_task_id_fkey FOREIGN KEY (review_task_id) REFERENCES public.moderation_tasks(id);

ALTER TABLE ONLY public.applications
    ADD CONSTRAINT applications_wish_id_wish_content_version_fkey FOREIGN KEY (wish_id, wish_content_version) REFERENCES public.wish_versions(wish_id, content_version);

ALTER TABLE ONLY public.assets
    ADD CONSTRAINT assets_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.blocks
    ADD CONSTRAINT blocks_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public.accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.blocks
    ADD CONSTRAINT blocks_target_id_fkey FOREIGN KEY (target_id) REFERENCES public.accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.care_cards
    ADD CONSTRAINT care_cards_recipient_id_fkey FOREIGN KEY (recipient_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.content_registry
    ADD CONSTRAINT content_registry_author_id_fkey FOREIGN KEY (author_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.content_restrictions
    ADD CONSTRAINT content_restrictions_content_id_fkey FOREIGN KEY (content_id) REFERENCES public.content_registry(id);

ALTER TABLE ONLY public.conversations
    ADD CONSTRAINT conversations_match_id_fkey FOREIGN KEY (match_id) REFERENCES public.matches(id);

ALTER TABLE ONLY public.deletion_jobs
    ADD CONSTRAINT deletion_jobs_subject_ref_fkey FOREIGN KEY (subject_ref) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.delivery_attempts
    ADD CONSTRAINT delivery_attempts_notification_id_fkey FOREIGN KEY (notification_id) REFERENCES public.notifications(id);

ALTER TABLE ONLY public.identity_lookup_claims
    ADD CONSTRAINT identity_lookup_claims_account_id_purpose_fkey FOREIGN KEY (account_id, purpose) REFERENCES public.identity_secrets(account_id, purpose) ON DELETE CASCADE;

ALTER TABLE ONLY public.identity_names
    ADD CONSTRAINT identity_names_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.identity_reviews
    ADD CONSTRAINT identity_reviews_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.identity_reviews
    ADD CONSTRAINT identity_reviews_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES public.assets(id);

ALTER TABLE ONLY public.identity_reviews
    ADD CONSTRAINT identity_reviews_reviewer_id_fkey FOREIGN KEY (reviewer_id) REFERENCES public.admin_accounts(id);

ALTER TABLE ONLY public.identity_secrets
    ADD CONSTRAINT identity_secrets_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.identity_secrets
    ADD CONSTRAINT identity_secrets_purpose_fkey FOREIGN KEY (purpose) REFERENCES public.identity_lookup_keys(purpose);

ALTER TABLE ONLY public.matches
    ADD CONSTRAINT matches_application_id_fkey FOREIGN KEY (application_id) REFERENCES public.applications(id);

ALTER TABLE ONLY public.matches
    ADD CONSTRAINT matches_helper_id_fkey FOREIGN KEY (helper_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.matches
    ADD CONSTRAINT matches_wish_id_requester_id_fkey FOREIGN KEY (wish_id, requester_id) REFERENCES public.wishes(id, owner_id);

ALTER TABLE ONLY public.messages
    ADD CONSTRAINT messages_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.conversations(id);

ALTER TABLE ONLY public.messages
    ADD CONSTRAINT messages_review_task_id_fkey FOREIGN KEY (review_task_id) REFERENCES public.moderation_tasks(id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.messages
    ADD CONSTRAINT messages_sender_id_fkey FOREIGN KEY (sender_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.moderation_scans
    ADD CONSTRAINT moderation_scans_crisis_version_fkey FOREIGN KEY (crisis_version) REFERENCES public.wordlist_versions(id);

ALTER TABLE ONLY public.moderation_scans
    ADD CONSTRAINT moderation_scans_ordinary_version_fkey FOREIGN KEY (ordinary_version) REFERENCES public.wordlist_versions(id);

ALTER TABLE ONLY public.moderation_scans
    ADD CONSTRAINT moderation_scans_school_version_fkey FOREIGN KEY (school_version) REFERENCES public.wordlist_versions(id);

ALTER TABLE ONLY public.moderation_scans
    ADD CONSTRAINT moderation_scans_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.moderation_tasks(id);

ALTER TABLE ONLY public.moderation_tasks
    ADD CONSTRAINT moderation_tasks_content_id_content_version_fkey FOREIGN KEY (content_id, content_version) REFERENCES public.content_registry(id, content_version);

ALTER TABLE ONLY public.moderation_tasks
    ADD CONSTRAINT moderation_tasks_reviewer_id_fkey FOREIGN KEY (reviewer_id) REFERENCES public.admin_accounts(id);

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_recipient_id_fkey FOREIGN KEY (recipient_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.outbox_events
    ADD CONSTRAINT outbox_events_work_id_fkey FOREIGN KEY (work_id) REFERENCES public.work_items(id);

ALTER TABLE ONLY public.penalties
    ADD CONSTRAINT penalties_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.penalties
    ADD CONSTRAINT penalties_content_id_fkey FOREIGN KEY (content_id) REFERENCES public.content_registry(id);

ALTER TABLE ONLY public.reports
    ADD CONSTRAINT reports_content_id_fkey FOREIGN KEY (content_id) REFERENCES public.content_registry(id);

ALTER TABLE ONLY public.reports
    ADD CONSTRAINT reports_handler_id_fkey FOREIGN KEY (handler_id) REFERENCES public.admin_accounts(id);

ALTER TABLE ONLY public.reports
    ADD CONSTRAINT reports_reporter_id_fkey FOREIGN KEY (reporter_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.resource_audits
    ADD CONSTRAINT resource_audits_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.admin_accounts(id);

ALTER TABLE ONLY public.resource_feedback
    ADD CONSTRAINT resource_feedback_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.resource_feedback
    ADD CONSTRAINT resource_feedback_resource_id_fkey FOREIGN KEY (resource_id) REFERENCES public.resource_materials(id);

ALTER TABLE ONLY public.resource_materials
    ADD CONSTRAINT resource_materials_category_id_fkey FOREIGN KEY (category_id) REFERENCES public.resource_categories(id);

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_admin_id_fkey FOREIGN KEY (admin_id) REFERENCES public.admin_accounts(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.story_versions
    ADD CONSTRAINT story_versions_review_task_id_fkey FOREIGN KEY (review_task_id) REFERENCES public.moderation_tasks(id);

ALTER TABLE ONLY public.story_versions
    ADD CONSTRAINT story_versions_wish_id_author_id_fkey FOREIGN KEY (wish_id, author_id) REFERENCES public.wishes(id, owner_id);

ALTER TABLE ONLY public.story_versions
    ADD CONSTRAINT story_versions_wordlist_version_fkey FOREIGN KEY (wordlist_version) REFERENCES public.wordlist_versions(id);

ALTER TABLE ONLY public.student_card_payloads
    ADD CONSTRAINT student_card_payloads_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES public.assets(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.student_login_exchanges
    ADD CONSTRAINT student_login_exchanges_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.sessions(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.whisper_aliases
    ADD CONSTRAINT whisper_aliases_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.whisper_aliases
    ADD CONSTRAINT whisper_aliases_post_id_fkey FOREIGN KEY (post_id) REFERENCES public.whisper_posts(id);

ALTER TABLE ONLY public.whisper_capsules
    ADD CONSTRAINT whisper_capsules_content_registry_id_fkey FOREIGN KEY (content_registry_id) REFERENCES public.content_registry(id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_capsules
    ADD CONSTRAINT whisper_capsules_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.whisper_capsules
    ADD CONSTRAINT whisper_capsules_review_task_id_fkey FOREIGN KEY (review_task_id) REFERENCES public.moderation_tasks(id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_comment_versions
    ADD CONSTRAINT whisper_comment_registry_version_fk FOREIGN KEY (content_registry_id, content_version) REFERENCES public.content_registry(id, content_version) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_comment_versions
    ADD CONSTRAINT whisper_comment_versions_comment_id_fkey FOREIGN KEY (comment_id) REFERENCES public.whisper_comments(id);

ALTER TABLE ONLY public.whisper_comment_versions
    ADD CONSTRAINT whisper_comment_versions_content_registry_id_fkey FOREIGN KEY (content_registry_id) REFERENCES public.content_registry(id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_comment_versions
    ADD CONSTRAINT whisper_comment_versions_review_task_id_fkey FOREIGN KEY (review_task_id) REFERENCES public.moderation_tasks(id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_comments
    ADD CONSTRAINT whisper_comments_author_id_fkey FOREIGN KEY (author_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.whisper_comments
    ADD CONSTRAINT whisper_comments_current_fk FOREIGN KEY (id, content_version) REFERENCES public.whisper_comment_versions(comment_id, content_version) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_comments
    ADD CONSTRAINT whisper_comments_post_id_fkey FOREIGN KEY (post_id) REFERENCES public.whisper_posts(id);

ALTER TABLE ONLY public.whisper_hugs
    ADD CONSTRAINT whisper_hugs_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.whisper_hugs
    ADD CONSTRAINT whisper_hugs_post_id_fkey FOREIGN KEY (post_id) REFERENCES public.whisper_posts(id);

ALTER TABLE ONLY public.whisper_nickname_versions
    ADD CONSTRAINT whisper_nickname_registry_version_fk FOREIGN KEY (content_registry_id, nickname_version) REFERENCES public.content_registry(id, content_version) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_nickname_versions
    ADD CONSTRAINT whisper_nickname_versions_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.whisper_nickname_versions
    ADD CONSTRAINT whisper_nickname_versions_content_registry_id_fkey FOREIGN KEY (content_registry_id) REFERENCES public.content_registry(id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_nickname_versions
    ADD CONSTRAINT whisper_nickname_versions_review_task_id_fkey FOREIGN KEY (review_task_id) REFERENCES public.moderation_tasks(id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_notification_refs
    ADD CONSTRAINT whisper_notification_refs_notification_id_fkey FOREIGN KEY (notification_id) REFERENCES public.notifications(id);

ALTER TABLE ONLY public.whisper_post_versions
    ADD CONSTRAINT whisper_post_registry_version_fk FOREIGN KEY (content_registry_id, content_version) REFERENCES public.content_registry(id, content_version) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_post_versions
    ADD CONSTRAINT whisper_post_versions_content_registry_id_fkey FOREIGN KEY (content_registry_id) REFERENCES public.content_registry(id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_post_versions
    ADD CONSTRAINT whisper_post_versions_post_id_fkey FOREIGN KEY (post_id) REFERENCES public.whisper_posts(id);

ALTER TABLE ONLY public.whisper_post_versions
    ADD CONSTRAINT whisper_post_versions_review_task_id_fkey FOREIGN KEY (review_task_id) REFERENCES public.moderation_tasks(id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_posts
    ADD CONSTRAINT whisper_posts_current_fk FOREIGN KEY (id, current_content_version) REFERENCES public.whisper_post_versions(post_id, content_version) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_posts
    ADD CONSTRAINT whisper_posts_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.whisper_posts
    ADD CONSTRAINT whisper_posts_published_fk FOREIGN KEY (id, published_content_version) REFERENCES public.whisper_post_versions(post_id, content_version) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_preferences
    ADD CONSTRAINT whisper_preferences_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.whisper_preferences
    ADD CONSTRAINT whisper_preferences_active_fk FOREIGN KEY (account_id, active_nickname_version) REFERENCES public.whisper_nickname_versions(account_id, nickname_version) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.whisper_preferences
    ADD CONSTRAINT whisper_preferences_candidate_fk FOREIGN KEY (account_id, candidate_nickname_version) REFERENCES public.whisper_nickname_versions(account_id, nickname_version) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.wish_versions
    ADD CONSTRAINT wish_versions_review_task_id_fkey FOREIGN KEY (review_task_id) REFERENCES public.moderation_tasks(id);

ALTER TABLE ONLY public.wish_versions
    ADD CONSTRAINT wish_versions_wish_id_fkey FOREIGN KEY (wish_id) REFERENCES public.wishes(id);

ALTER TABLE ONLY public.wish_versions
    ADD CONSTRAINT wish_versions_wordlist_fk FOREIGN KEY (wordlist_version) REFERENCES public.wordlist_versions(id);

ALTER TABLE ONLY public.wishes
    ADD CONSTRAINT wishes_current_content_fk FOREIGN KEY (id, current_content_version) REFERENCES public.wish_versions(wish_id, content_version) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY public.wishes
    ADD CONSTRAINT wishes_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public.accounts(id);

ALTER TABLE ONLY public.wordlist_entries
    ADD CONSTRAINT wordlist_entries_version_id_fkey FOREIGN KEY (version_id) REFERENCES public.wordlist_versions(id);

ALTER TABLE ONLY public.wordlist_heads
    ADD CONSTRAINT wordlist_heads_effective_id_fkey FOREIGN KEY (effective_id) REFERENCES public.wordlist_versions(id);

ALTER TABLE ONLY public.wordlist_versions
    ADD CONSTRAINT wordlist_versions_base_effective_id_fkey FOREIGN KEY (base_effective_id) REFERENCES public.wordlist_versions(id);
