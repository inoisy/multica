-- Fork schema: allow issue.origin_type 'express_chat' for issues created by
-- the eXpress (BotX) `/issue` command. origin_id stores the chat_session id,
-- like the other channel origins. Same widen-only pattern as upstream 366/367:
-- recreate the CHECK as NOT VALID (short lock), then validate.
--
-- Keep in sync on upstream syncs: a later upstream migration that redefines
-- issue_origin_type_check must keep 'express_chat' in the list (re-add it in
-- the next fork migration), or new eXpress /issue inserts start failing.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat', 'agent_create', 'dingtalk_chat', 'wecom_chat', 'telegram_chat', 'express_chat'))
    NOT VALID;
ALTER TABLE issue VALIDATE CONSTRAINT issue_origin_type_check;
