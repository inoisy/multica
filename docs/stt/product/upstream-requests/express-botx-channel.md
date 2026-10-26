Title: Add an eXpress / BotX channel through the existing channel abstraction

## User problem

Teams using eXpress should be able to talk to a Multica agent from their existing
messenger and turn a conversation into an issue. Switching to a separate web
application interrupts that workflow. A channel adapter should reuse Multica's
agent permissions, conversation lifecycle, and issue creation behavior.

## Proposed scope

- Add an eXpress / BotX adapter beside the existing channel adapters.
- Bind the external sender identity to a workspace member before allowing an
  agent invocation; unbound senders receive setup guidance.
- Support inbound text, an outbound reply, and the existing conversation-to-issue
  action. Preserve conversation and reply correlation across delivery retries.
- Configure one bot installation through the existing integration flow. Store
  credentials through the existing encrypted-secret mechanism.
- Authenticate incoming requests according to the supported BotX/CTS version.
  Document supported versions and delivery semantics before implementation.
- Reuse server-side agent execution settings and authorization. The channel does
  not select a different model, acquire extra permissions, or expose a host shell.

Start with text. File and image behavior should follow the shared channel media
contract discussed in [#6566](https://github.com/multica-ai/multica/issues/6566),
rather than adding an adapter-specific attachment path. Automatic issue progress
notifications can be a subsequent change.

## Acceptance criteria

1. A bound member sends a message and receives a reply in the same conversation.
2. An unbound or unauthorized sender cannot invoke the agent or read its context.
3. An invalid webhook authentication attempt is rejected before any work is queued.
4. Repeated inbound delivery does not create duplicate runs or issues.
5. An issue created from the conversation has the correct workspace, author, and
   conversation link; subsequent replies do not cross conversation boundaries.
6. Provider unavailability produces a visible delivery failure and bounded retry.
7. Fake BotX transport tests cover authentication, identity binding, duplicate
   delivery, reply correlation, timeouts, and permission failures without real
   model calls. Setup documentation lists version and connectivity prerequisites.

Would maintainers accept this adapter in the core channel registry, or prefer a
plugin using the same channel interfaces?
