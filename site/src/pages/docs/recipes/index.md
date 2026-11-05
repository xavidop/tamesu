---
layout: ../../../layouts/DocsLayout.astro
title: Recipes
---

# Recipes: complete suites to start from

Each recipe is a whole project: a `tamesu.yaml` and a suite you can copy, change the URLs and
run. They validate as written (`tamesu validate`), and together they use most of what the
[YAML reference](/docs/suites/) lists. They are grouped by what is under test.

## [Chat and web agents](/docs/recipes/chat-and-web/)

A REST agent gated for release, a web shop in the browser, the backend behind the transcript, and escalation.

- [A support agent over REST, gated for release](/docs/recipes/chat-and-web/#a-support-agent-over-rest-gated-for-release)
- [A web shop in the browser: mocks, accessibility, visual baseline](/docs/recipes/chat-and-web/#a-web-shop-in-the-browser-mocks-accessibility-visual-baseline)
- [Verify the backend, not just the transcript](/docs/recipes/chat-and-web/#verify-the-backend-not-just-the-transcript)
- [Escalation: when the agent must hand off, and when it must not](/docs/recipes/chat-and-web/#escalation-when-the-agent-must-hand-off-and-when-it-must-not)

## [Voice agents](/docs/recipes/voice/)

The OpenAI Realtime protocol, a phone IVR over SIP, and Vapi, Deepgram and Retell agents on the generic targets.

- [A voice agent on the OpenAI Realtime protocol](/docs/recipes/voice/#a-voice-agent-on-the-openai-realtime-protocol)
- [A phone IVR through the SIP target](/docs/recipes/voice/#a-phone-ivr-through-the-sip-target)
- [A Vapi assistant over its websocket transport](/docs/recipes/voice/#a-vapi-assistant-over-its-websocket-transport)
- [A Deepgram Voice Agent](/docs/recipes/voice/#a-deepgram-voice-agent)
- [A Retell agent through a web call](/docs/recipes/voice/#a-retell-agent-through-a-web-call)

## [Models, tools and agents](/docs/recipes/models-and-agents/)

A prompt and its tools against a model, an MCP server through the model that uses it, and an orchestrator with the agents it delegates to.

- [A prompt and its tools against a model, grounded and under failure](/docs/recipes/models-and-agents/#a-prompt-and-its-tools-against-a-model-grounded-and-under-failure)
- [An MCP server, through the model that uses it](/docs/recipes/models-and-agents/#an-mcp-server-through-the-model-that-uses-it)
- [An orchestrator and the agents it delegates to](/docs/recipes/models-and-agents/#an-orchestrator-and-the-agents-it-delegates-to)

## [Messaging channels](/docs/recipes/channels/)

Slack, Teams, Telegram and WhatsApp bots, and a Voiceflow agent, all over the REST target with asynchronous replies.

- [A Slack bot, as a user in the workspace](/docs/recipes/channels/#a-slack-bot-as-a-user-in-the-workspace)
- [A Teams or Copilot Studio bot, through Direct Line](/docs/recipes/channels/#a-teams-or-copilot-studio-bot-through-direct-line)
- [A Telegram bot, with tamesu as the Bot API](/docs/recipes/channels/#a-telegram-bot-with-tamesu-as-the-bot-api)
- [A WhatsApp bot, with tamesu as the Cloud API](/docs/recipes/channels/#a-whatsapp-bot-with-tamesu-as-the-cloud-api)
- [A Voiceflow agent over its Conversations API](/docs/recipes/channels/#a-voiceflow-agent-over-its-conversations-api)

## [Pipelines and operations](/docs/recipes/pipeline/)

Red-team gating without a model key, a nightly red team, one suite for every environment and language, and a quarantined flaky test.

- [Red-team gating without a model key in the pipeline](/docs/recipes/pipeline/#red-team-gating-without-a-model-key-in-the-pipeline)
- [A nightly red team with playbooks](/docs/recipes/pipeline/#a-nightly-red-team-with-playbooks)
- [One suite, every environment and every language](/docs/recipes/pipeline/#one-suite-every-environment-and-every-language)
- [A known flaky test, quarantined until the fix lands](/docs/recipes/pipeline/#a-known-flaky-test-quarantined-until-the-fix-lands)
