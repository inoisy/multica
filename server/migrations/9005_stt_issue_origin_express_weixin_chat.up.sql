-- Fork schema: upstream 667 redefined issue_origin_type_check for
-- 'weixin_chat' without the fork's 'express_chat' (9004), and on a fresh
-- database 9004 runs after 667 and drops 'weixin_chat'. Recreate the CHECK
-- with both. Same widen-only pattern: NOT VALID (short lock), then validate.
--
-- Keep in sync on upstream syncs: a later upstream migration that redefines
-- issue_origin_type_check must keep 'express_chat' in the list (re-add it in
-- the next fork migration), or new eXpress /issue inserts start failing.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat', 'agent_create', 'dingtalk_chat', 'wecom_chat', 'telegram_chat', 'weixin_chat', 'express_chat'))
    NOT VALID;
ALTER TABLE issue VALIDATE CONSTRAINT issue_origin_type_check;
