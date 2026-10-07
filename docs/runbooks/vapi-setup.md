# Vapi setup

How to connect HaalChaal to Vapi for the first allowlisted test calls (M8). Keep `CALLS_ENABLED=false` until every step is done, and `TELECOM_COMPLIANCE_ACK=false` until legal sign-off (SPEC §14).

Checked against docs.vapi.ai on 2026-10-08. Vapi changes quickly, so if a screen looks different, follow the current docs and update this page.

## How it fits together

- HaalChaal places each call with `POST https://api.vapi.ai/call`, using a **saved assistant** and a **saved phone number**. The rendered agent prompt (SPEC §7) goes into `assistantOverrides.variableValues.system_prompt`, and our call id goes into `assistantOverrides.metadata.haalchaal_call_id`.
- Each call also turns recording and SIP packet capture off, and caps calls at 10 minutes. HaalChaal only uses the transcript.
- Vapi posts status updates and the end-of-call report to the assistant's server URL. It posts the three tool calls to their own server URLs.
- Vapi attaches the authentication credential **only** to server URLs saved in Vapi (an assistant, a tool, a phone number or the org settings), never to a URL sent in the call request. That is why every URL below is configured in the dashboard, and why HaalChaal never sends one.
- Requests are authenticated with a shared secret, not a signature. The secret must be long and random, and the server must be behind TLS.

## 1. Webhook secret

1. Generate a secret: `openssl rand -hex 32` (64 characters; HaalChaal refuses fewer than 32).
2. In the Vapi dashboard, go to **Integrations → Server Configuration → Add Custom Credential** and choose **Bearer Token**.
   - Name: `haalchaal-prod` (or `-staging`)
   - Token: the secret
   - Header: `Authorization`, with the Bearer prefix on. (`X-Vapi-Secret` without the prefix also works.)
3. Note the credential id (`cred_...`).
4. Put the same secret in `VOICE_WEBHOOK_SECRET`. Use a different secret per environment.

## 2. Phone number

Import the calling number (for India, usually through a SIP trunk or a supported telephony provider; see Vapi's phone-number docs). Note its **phone number id** for `VOICE_PHONE_NUMBER_ID`, and the number itself, in E.164, for `VOICE_FROM_NUMBER`.

Before any real call, the telecom route needs legal sign-off as a service call (SPEC §14). Until then, dial only founders' allowlisted numbers from staging.

## 3. Tools

Create three **Function** tools from `prompts/voice_tools.json`, copying each tool's name, description and parameters exactly. Give each one the server URL `https://<host>/v1/voice/tools/<tool name>` and the credential from step 1.

- Make each tool synchronous (not async), so the agent hears "Reported" before it carries on.
- A test keeps `prompts/voice_tools.json` in step with what the handlers understand. Any category HaalChaal doesn't recognise becomes `other`, and any severity it doesn't recognise becomes `emergency`, so a stale copy in Vapi errs on the cautious side. After changing the file, update the tools in Vapi anyway.
- If a tool has no server URL of its own, Vapi sends it to the assistant's URL instead. HaalChaal handles it there too, but configure the tool URLs anyway.

## 4. Assistant

Create one assistant per environment:

- **System prompt:** exactly `{{system_prompt}}`, and nothing else. HaalChaal renders the whole prompt from `prompts/agent_system.tmpl`, including the safety rules. Never add instructions here: they would bypass review and the golden tests.
- **First message:** leave it empty, or set the mode to "assistant speaks first with a model-generated message". The prompt already tells the agent to say it is an AI.
- **Model, voice, transcriber:** whatever won the bake-off for Hindi, Tamil and English (SPEC §17). Set a fallback for each.
- **Tools:** the three tools from step 3.
- **Server URL:** `https://<host>/v1/webhooks/voice/vapi`, with the credential from step 1.
- **Server messages:** at least `status-update`, `end-of-call-report` and `tool-calls`. HaalChaal ignores the others, so leave them out to cut traffic.
- **Recording:** off. HaalChaal turns it off on every call anyway.
- **Voicemail detection:** on, if available. A voicemail counts as an unanswered attempt and is retried.

Note the assistant id for `VOICE_AGENT_ID`.

## 5. Data retention at Vapi

Vapi stores each call's transcript and logs on its side. HaalChaal's daily retention job deletes the Vapi call (`DELETE /call/{id}`) once our transcript passes `delete_after`. Decide with the lawyer whether to also do any of the following (SPEC §17, retention):

- turn on Vapi's Zero Data Retention for the organisation;
- shorten `RETENTION_TRANSCRIPT_DAYS`;
- record Vapi as a processor in `docs/processors.md`, with the region its data is stored in.

## 6. Configure HaalChaal

```
VOICE_PROVIDER=vapi
VOICE_API_KEY=<private API key>
VOICE_WEBHOOK_SECRET=<step 1>
VOICE_AGENT_ID=<step 4>
VOICE_PHONE_NUMBER_ID=<step 2>
VOICE_FROM_NUMBER=+91...
VOICE_COST_PAISE_PER_USD=<current rate x 100, e.g. 8500; 0 to skip>
DEV_ALLOWLIST=<founder phones>
CALLS_ENABLED=false
```

Start the server. `serve` refuses to start if any of the Vapi settings is missing or the secret is too short.

## 7. First test call

1. Onboard a founder as a test parent through the admin pages, and record consent.
2. Set `CALLS_ENABLED=true` in **staging**, with the founder's number in `DEV_ALLOWLIST`.
3. Use **Test call** on the parent's page. Check each of these:
   - The admin page shows the attempt going dialing → ringing → in progress → completed.
   - The agent says it is an AI in the first sentence.
   - Saying "main gir gayi" during the call raises an emergency alert in the review queue within seconds (tool call), and the WhatsApp alert follows.
   - After hanging up, the WhatsApp summary arrives within 5 minutes (the M8 "done when").
   - Saying "mujhe call mat karo" pauses the parent.
   - In Vapi's webhook logs, every delivery got a 200, and there is no "credentials withheld" entry.
4. Set `CALLS_ENABLED=false` again until the next session.

## Troubleshooting

- **401 on every webhook:** the credential isn't attached. Check that the URL is saved on the assistant or tool, not passed per call, and that the token matches `VOICE_WEBHOOK_SECRET` exactly.
- **404 "unknown call":** the webhook names a call this environment never placed. Usually staging and prod share an assistant; use one assistant per environment.
- **Call stuck in dialing:** no status updates are arriving. Check the assistant's server messages. The stale-call sweeper fails the attempt after 30 minutes, and the retry policy takes over.
- **The agent says "no result returned" after a tool:** the tool URL or credential is wrong. The alert still comes from the transcript after the call, but late. Fix this before calling real parents.
