---
layout: ../../../layouts/DocsLayout.astro
title: "Recipes: Messaging channels"
---

# Messaging channels

Slack, Teams, Telegram and WhatsApp bots, and a Voiceflow agent, all over the REST target with asynchronous replies.

Each recipe is a whole project you can copy, change the URLs and run; see [Recipes](/docs/recipes/) for the other pages.

Unless a recipe shows its own, they run with this project:

```yaml
# tamesu.yaml
version: 1
suites: ["*.yaml"]
genkit: { plugins: [googlegenai] }
models: { default: googleai/gemini-2.5-flash }
secrets:
  GEMINI_API_KEY: env:GEMINI_API_KEY
```

## A Slack bot, as a user in the workspace

Slack lets a program act as a person: with a user token (`xoxp-`, scopes `chat:write`,
`channels:history` or `groups:history`, `im:write`, `im:history`) tamesu posts to a channel and
reads the thread back. Each test is its own thread: the turn's request posts the message,
`request.capture` keeps its timestamp, and the poll reads the replies under it. The bot's
messages are the ones with a `bot_id`, so the JSONPath filter leaves the user's own message out.

```yaml
suite: slack-bot
tags: [slack, nightly]
vars: { channel: C0123456789 }
secrets: { SLACK_USER_TOKEN: env:SLACK_USER_TOKEN }
targets:
  slack:
    type: rest
    url: https://slack.com/api/chat.postMessage
    headers: { Authorization: "Bearer {{secrets.SLACK_USER_TOKEN}}" }
    request:
      body: { channel: "{{vars.channel}}", text: "{{message}}", thread_ts: "{{session.thread}}" }
      capture: { ts: $.ts }                       # the message just posted; the bot answers after it
    response:
      text: "$.messages[?(@.bot_id && @.ts != '{{sent.ts}}')].text"
      poll:
        url: https://slack.com/api/conversations.replies?channel={{vars.channel}}&ts={{session.thread}}&oldest={{sent.ts}}
        headers: { Authorization: "Bearer {{secrets.SLACK_USER_TOKEN}}" }
        interval: 1s
        timeout: 30s
        idle: 3s                                   # the bot may answer in two messages
    session:
      open:                                        # the thread's root message, posted once
        url: https://slack.com/api/chat.postMessage
        headers: { Authorization: "Bearer {{secrets.SLACK_USER_TOKEN}}" }
        body: { channel: "{{vars.channel}}", text: "tamesu test {{session}}" }
        capture: { thread: $.ts }
tests:
  - name: order status
    target: slack
    persona: { name: Marta, style: "polite, direct" }
    scenario: { goal: "Ask where order ORD-1001 is and when it arrives", max_turns: 4 }
    assert:
      - contains: "ORD-1001"
      - judge: "The agent gives a delivery estimate or says how to get one"
      - json_path: { path: "$.messages[?(@.bot_id)].blocks" }   # the answer uses Block Kit
      - metric: { name: turn_latency_ms_p95, lt: 8000 }
```

Without a cursor the poll returns the whole thread every time, so a poll replaces the one before
it and the turn keeps the latest state; `oldest` keeps earlier turns out. Slack's history scopes
need the app installed in the workspace, and the token belongs to the test user, so keep it in a
secret and the channel private to the team.

## A Teams or Copilot Studio bot, through Direct Line

Bot Framework bots, including the ones Teams and Copilot Studio run, expose Direct Line: a
channel made for custom clients, enabled in the bot's Azure channels with a secret. One request
starts the conversation, each turn posts an activity, and the replies are read with a watermark so
every poll returns only what is new. The test exercises the bot's logic end to end; Teams-only
cards and channel data render differently in Teams itself.

```yaml
suite: teams-bot
tags: [teams]
vars: { dl: https://directline.botframework.com/v3/directline }
secrets: { DIRECT_LINE_SECRET: env:DIRECT_LINE_SECRET }
targets:
  teams:
    type: rest
    url: "{{vars.dl}}/conversations/{{session.id}}/activities"
    headers: { Authorization: "Bearer {{session.token}}" }
    session:
      id: "user-{{uuid}}"
      open:
        url: "{{vars.dl}}/conversations"
        headers: { Authorization: "Bearer {{secrets.DIRECT_LINE_SECRET}}" }
        body: { user: { id: "{{session}}", name: Marta } }
        capture: { id: $.conversationId, token: $.token }
    request:
      body: { type: message, from: { id: "{{session}}", name: Marta }, text: "{{message}}", locale: en-US }
    response:
      text: "$.activities[?(@.type == 'message' && @.from.id != '{{session}}')].text"
      done: "$.activities[?(@.type == 'endOfConversation')].type"
      poll:
        url: "{{vars.dl}}/conversations/{{session.id}}/activities?watermark={{cursor}}"
        headers: { Authorization: "Bearer {{session.token}}" }
        cursor: $.watermark
        interval: 500ms
        idle: 2s
tests:
  - name: book a meeting room
    target: teams
    persona: { name: Marta, style: "busy, types short messages" }
    scenario: { goal: "Book a room for 4 people tomorrow at 10", max_turns: 6 }
    assert:
      - judge: "The agent confirms a room, a date and a time, or explains why it cannot"
      - json_path: { path: "$.activities[*].attachments[?(@.contentType == 'application/vnd.microsoft.card.adaptive')]" }
        on_turn: last
      - not_contains: "Sorry, something went wrong"
  - name: hands off when asked for a person
    target: teams
    persona: { name: Ken, style: "frustrated" }
    scenario: { goal: "Insist on talking to a human about a billing error", max_turns: 4 }
    assert:
      - handoff: { within_turns: 3 }
```

The handoff comes from the target's `handoff` signal (a `json_path` over the activities, such as
an `event` activity named `handoff.initiate`), declared on the target as for any other type.

## A Telegram bot, with tamesu as the Bot API

Telegram's Bot API only lets a program act as the bot, so tamesu plays the platform instead: it
sends the bot an `Update` on its webhook and serves the Bot API the bot answers through. Point the
bot at the listener (every Bot API library takes a base URL: `TELEGRAM_API_URL`, `apiURL`,
`base_url`), run the bot, and each test is one chat. Requests to other methods (`sendChatAction`,
`getMe`) are acknowledged with `reply` and ignored; `sendMessage` carries the turn. Telegram ids
are integers, so the session id is a `{{faker.number}}` and the body sends it through the
`number` filter; `$.chat_id` routes the replies when tests run in parallel.

```yaml
suite: telegram-bot
tags: [telegram]
vars: { webhook: "${BOT_WEBHOOK:-http://localhost:3000/telegram/webhook}" }
targets:
  telegram:
    type: rest
    url: "{{vars.webhook}}"
    session: { id: "{{faker.number}}" }
    request:
      body:
        update_id: 1
        message:
          message_id: 1
          date: 1700000000
          chat: { id: "{{session|number}}", type: private, first_name: Marta }
          from: { id: "{{session|number}}", is_bot: false, first_name: Marta, language_code: en }
          text: "{{message}}"
    response:
      text: $.text
      listen:
        addr: 127.0.0.1:8089                       # the bot's Bot API base URL is http://127.0.0.1:8089
        path: "POST /bot*/sendMessage"
        session: $.chat_id
        reply: { ok: true, result: { message_id: 1, date: 1700000000, chat: { id: "{{session|number}}", type: private }, text: "" } }
        timeout: 20s
        idle: 1s
tests:
  - name: start and ask for help
    target: telegram
    persona: { name: Marta, style: "new to the bot" }
    scenario:
      goal: "Find out what the bot can do, then ask for the opening hours"
      max_turns: 4
      script:
        - say: "/start"
    assert:
      - judge: "The first reply explains what the bot does"
      - contains: "hours"
      - json_path: { path: $.reply_markup }      # the bot offered a keyboard
        on_turn: 1
```

The bot never touches Telegram during the run, so this suite runs in CI with no token and no
phone number. What it does not cover is Telegram itself: delivery, rate limits and how the
keyboard looks on a phone.

## A WhatsApp bot, with tamesu as the Cloud API

The WhatsApp Cloud API has no consumer side either, so the shape is the same as Telegram's:
tamesu posts the webhook payload Meta would send for an incoming message, and serves the Graph API
the bot replies through. Set the bot's Graph API base URL (`https://graph.facebook.com` in most
SDKs) to `{{listen.url}}`. Read receipts and typing indicators go to the same `/messages` path,
and are ignored because they carry no text; template and interactive messages still land in the
turn's raw payload for `json_path`.

```yaml
suite: whatsapp-bot
tags: [whatsapp]
vars:
  webhook: "${BOT_WEBHOOK:-http://localhost:3000/whatsapp/webhook}"
  phone_id: "123456789012345"
targets:
  whatsapp:
    type: rest
    url: "{{vars.webhook}}"                        # disable the webhook signature check in the test environment
    session: { id: "34{{faker.number}}" }          # the user's WhatsApp number, digits only
    request:
      body:
        object: whatsapp_business_account
        entry:
          - id: "{{vars.phone_id}}"
            changes:
              - field: messages
                value:
                  messaging_product: whatsapp
                  metadata: { display_phone_number: "15550000000", phone_number_id: "{{vars.phone_id}}" }
                  contacts: [{ profile: { name: Marta }, wa_id: "{{session}}" }]
                  messages: [{ from: "{{session}}", id: "wamid.{{uuid}}", timestamp: "1700000000", type: text, text: { body: "{{message}}" } }]
    response:
      text: "$.text.body"
      listen:
        addr: 127.0.0.1:8090                       # the bot's Graph API base URL is http://127.0.0.1:8090
        path: "POST /*/messages"                   # /v21.0/{phone_id}/messages
        session: $.to
        reply: { messaging_product: whatsapp, contacts: [{ wa_id: "{{session}}" }], messages: [{ id: "wamid.{{uuid}}" }] }
        idle: 1s
tests:
  - name: return window
    target: whatsapp
    persona: { name: Marta, style: "polite" }
    scenario: { goal: "Ask whether shoes bought 40 days ago can be returned", max_turns: 4 }
    assert:
      - contains: "30 days"
      - judge: "The agent states the return window and does not promise an exception"
      - json_path: { path: "$.interactive.action.buttons" }   # the answer offered buttons
        on_turn: 1
```

The bots behind both simulators are the real ones, with their real prompts, state and backend,
so the assertions, captures, grounding checks and packs of any other suite apply unchanged; what
stays outside the test is the platform between the user and the bot.

## A Voiceflow agent over its Conversations API

Voiceflow's REST API runs one turn per request: `PUT /v1/stable/conversation/{userID}` with the
action, answering the agent's traces. `session.open` launches the conversation, `method: PUT`
sends each text action, the JSONPath over the traces joins the agent's messages, and the `end`
trace closes the session. Variables and a state reset are `before_each` HTTP hooks.

```yaml
suite: voiceflow-agent
tags: [voiceflow]
vars: { project_id: "${VF_PROJECT_ID:-proj_000}", base: https://realtime-api.voiceflow.com/v1/stable }
secrets: { VF_TOKEN: env:VF_TOKEN }
hooks:
  before_each:
    - http:
        url: "{{vars.base}}/conversation-state/{{session}}/variables?projectID={{vars.project_id}}&environmentAlias=main&version=published"
        method: PATCH
        headers: { Authorization: "Bearer {{secrets.VF_TOKEN}}" }
        body: { variables: { customer_name: Marta } }
targets:
  agent:
    type: rest
    url: "{{vars.base}}/conversation/{{session}}?projectID={{vars.project_id}}&environmentAlias=main"
    method: PUT
    headers: { Authorization: "Bearer {{secrets.VF_TOKEN}}" }
    session:
      open:                                         # launch the agent; its opening words are traces too
        url: "{{vars.base}}/conversation/{{session}}?projectID={{vars.project_id}}&environmentAlias=main"
        method: PUT
        headers: { Authorization: "Bearer {{secrets.VF_TOKEN}}" }
        body: { action: { type: launch, payload: {} }, version: published }
    request:
      body: { action: { type: text, payload: "{{message}}" }, version: published }
    response:
      text: "$.traces[?(@.type=='text' || @.type=='speak')].payload.message"
      join: "\n"
      done: "$.traces[?(@.type=='end')].type"
tests:
  - name: return after 40 days
    target: agent
    persona: { name: Marta, style: "polite, slightly confused" }
    scenario: { goal: "Find out if the shoes bought 40 days ago can still be returned", max_turns: 6 }
    assert:
      - judge: "The agent states the 30-day window and does not promise an exception"
      - json_path: { path: "$.traces[?(@.type=='choice')]" }       # the first reply offered buttons
        on_turn: 1
```

`version: draft` runs what you are editing on a project that was never published. The same
agent through Voiceflow's live **WebSocket runtime**, a socket.io server, is the handshake
recipe of the [REST target](/docs/targets/rest/#a-socketio-server-and-a-handshake) with the
project's API key as `authorization`, `action.trace` events as the text and `session.ended` as
`done`.
