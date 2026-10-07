# HaalChaal: Project Overview

*Status as of 8 October 2026. Read this first. Technical detail lives in `docs/SPEC.md`, progress notes in `docs/progress.md`, and step-by-step guides in `docs/runbooks/`.*

---

## 1. Summary

**HaalChaal** ("how are you?") is an AI service that phones elderly parents once a day, in their own language, to ask how they are. It talks to them about their health, sleep, food and medicines, remembers what they said in earlier calls, and sends their children a short WhatsApp summary after every call. If something sounds wrong during a call, the family gets an alert straight away. That covers a fall, chest pain, breathing trouble, signs of a stroke, talk of self-harm, or a scam caller asking for money or an OTP.

The parent needs nothing but an ordinary phone and installs nothing. The family uses WhatsApp, which they already have. The founders run everything from a small, secure admin website.

**Where we are:** the whole backend is built and tested. That covers calling, safety rules, the post-call AI report, WhatsApp alerts and summaries, memory between calls, the admin website, security and operations. It runs on the founder's PC today with simulated phone calls and messages. The code connecting it to the real services (Vapi for calls, Claude for reports, WhatsApp) is written and tested but not yet switched on.

**What's next:** create the real accounts (Vapi, an Indian number, WhatsApp, Anthropic, hosting), make one real test call to a founder, get the legal and medical checks done, then run a friendly trial and a 12-week pilot with 10 to 30 families.

---

## 2. The idea, and why it matters

### The problem
- Millions of Indian parents aged 60 to 85 live alone or with only their spouse, while their children work in other cities or abroad.
- Children worry but can't call every day, and when they do call, parents often say "sab theek hai" (all is fine) and hide problems.
- A fall, a missed medicine or a scam call can go unnoticed for days.
- Elderly people are now prime targets of "digital arrest" and OTP scams.
- Existing options don't fit well:
  - smartwatches and apps need tech skills and charging;
  - full-time caregivers are expensive;
  - panic buttons depend on the parent pressing them in time.

### Our solution
A warm, patient AI voice calls every day at the parent's chosen time, in Hindi, Tamil or English. It:
- checks on mood, sleep, appetite, pain and medicines, without ever giving medical advice;
- remembers yesterday ("How is your knee today?");
- spots danger signs and scam patterns while the call is still going on;
- sends the family a short daily summary, and an immediate alert when needed.

### Why it's important
- **Safety:** emergencies are caught within minutes, not days. A false alarm is acceptable; a missed emergency is not.
- **Peace of mind:** children know every day how their parents are.
- **Dignity and companionship:** a friendly daily conversation without feeling watched. Parents can keep small things private from the family.
- **Scam protection:** the AI recognises scam stories and warns both parent and family.
- **Accessible:** it works on any phone, needs no smartphone, no app and no internet for the parent, and speaks Indian languages.

### Gaps it fills
| Gap today | How HaalChaal fills it |
| --- | --- |
| Parents need tech skills for apps or devices | Just answer a normal phone call |
| Children can't call every day | An automatic daily call and WhatsApp summary |
| Problems are hidden or forgotten | A structured daily check that remembers earlier calls |
| Late discovery of falls and illness | Instant alerts, escalated step by step until someone responds |
| Scams aimed at the elderly | Scam detection and warnings during the call |
| Caregivers are costly | Low cost per family (call minutes plus AI) |
| Most voice tools are English-only | Hindi and English now, Tamil next |

---

## 3. How it works

1. **Onboarding (admin website):** a founder enters the parent, family members, medicines and call time, and records consent in person.
2. **Daily call:** at the chosen time, within a safe window of 9 am to 8 pm, the system calls the parent through the voice platform (Vapi). The AI says clearly that it is an AI.
3. **During the call:** if a danger sign or scam comes up, the AI reports it at once. An alert is created immediately and the family's WhatsApp alert starts.
4. **After the call:** the transcript is turned into a structured report by an AI model (Claude) and checked by our own rules and a keyword detector, which acts as a second safety net.
5. **Family update:** a short WhatsApp summary goes to the family. Alerts escalate in turn: first child → other children → a local contact (neighbour) → the founders, until someone taps "I'm on it".
6. **Human review:** every emergency, scam alert or uncertain report goes to the founders' review queue.
7. **Memory:** follow-ups ("knee pain") come up in the next day's call, and expire after a week.
8. **If nobody answers:** the system retries twice (after 15 and 45 minutes). If all three attempts fail, the family gets a missed-call alert.

### Who uses what
| Person | What they use |
| --- | --- |
| Parent | An ordinary phone call |
| Family member | WhatsApp: summaries, alerts, an "I'm on it" button |
| Founders (admin) | Admin website: dashboard, onboarding, review queue, call details, pause/stop, test call, data export and erase |

### Technology (for reference)
- **Language and database:** Go, with PostgreSQL. The job queue also runs on PostgreSQL, so no extra systems are needed.
- **Voice:** Vapi (chosen), which runs the voice, speech recognition and conversation model.
- **Reports:** Anthropic Claude reads the transcript and writes the report.
- **Messaging:** the WhatsApp Business Cloud API (Meta).
- **Admin website:** simple server-rendered pages with strong security.
- **Packaging:** a Docker image for deployment.

---

## 4. What we have built

**Status: built and tested.** There are automated tests for every part, and all of them pass.

| Area | What's done |
| --- | --- |
| Calling engine | Daily scheduling per parent's timezone and call window; daily or 3-days-a-week plans; retries; stale-call clean-up; never calls outside the window or without consent |
| Safety rules (non-negotiable) | The AI always says it's an AI; no medical advice; red flags always alert; scams detected; stop requests pause calls at once; private notes never reach the family; nothing is sent unless `CALLS_ENABLED=true`; production calling also needs a legal sign-off switch (`TELECOM_COMPLIANCE_ACK`) |
| Post-call report | A strict report format; checks that the AI's output is valid; a backup keyword detector (Hindi in Latin and Devanagari script, English, Tamil words); uncertain reports go to human review |
| Alerts | Emergency, urgent, scam, missed-call and "watch" (trend) alerts; step-by-step escalation with timeouts; "I'm on it" acknowledgement |
| WhatsApp | Message templates; delivery tracking; inbound replies stored securely; Hindi wording for messages |
| Memory | Follow-ups carried into the next call, expiring after 7 days |
| Admin website | Login with lockout after failed attempts; dashboard metrics (pickup rate, call minutes, cost per family, time to summary); onboarding form; review queue; call detail (every view is logged); consents; pause, stop and resume; medicines; test call; data export; data erase |
| Privacy and security | Health data encrypted in the database; phone numbers masked in logs; no transcripts in logs; an audit log of admin actions; key rotation; security reviews done at checkpoints |
| Operations | Daily data-retention clean-up; Docker image; automated checks on GitHub; runbooks for deployment, backups, key rotation, incidents and data breaches |
| Vendor connections | **Vapi** voice adapter, **Claude** report adapter and **WhatsApp Cloud** adapter: all written and tested, not yet switched on |
| Testing | 24 recorded test conversations (falls, chest pain, scams, stop requests, a stranger answering, prompt-injection attempts and more) replayed through the whole system; a load test with 1,000 parents |
| Local demo | Runs on the founder's PC; `make seed-demo` adds four demo families |

### What works right now
- The full system on your PC at http://localhost:8080/admin/, with simulated calls and messages.
- Onboarding families, managing consents and medicines, pausing and stopping calls, the review queue and the dashboard.
- Running a full simulated call: `make simcall SCENARIO=fall_emergency` shows call → report → alert → WhatsApp.

### Not working yet (needs accounts or approvals, not code)
- Real phone calls, which need Vapi, an Indian number and a legal sign-off.
- Real WhatsApp messages, which need Meta approval of the templates.
- Real AI reports, which need an Anthropic API key and a legal check on sending data abroad.
- Public online access, which needs hosting.

---

## 5. Gaps remaining in the current project

| # | Gap | Type | Impact |
| --- | --- | --- | --- |
| 1 | No real call made yet (Vapi account, Indian number and assistant not set up) | Setup | Blocks the pilot |
| 2 | Voice, speech recognition and model on Vapi not chosen or tested for Hindi, Tamil and English | Decision and testing | Call quality |
| 3 | WhatsApp templates not approved by Meta | Approval | No family messages |
| 4 | Not hosted online (India region, managed database, backups, HTTPS, uptime alerts) | Setup | Can't go live |
| 5 | Backup restore not yet tested on real hosting | Operations | Data safety |
| 6 | Legal sign-off: telecom (automated service calls), DPDP Act consent script, retention periods, sending data to AI services outside India, Vapi's data region | Legal | Production calling stays blocked |
| 7 | Red-flag list and alert timings not reviewed by a doctor; helpline numbers (112, Tele-MANAS 14416) need reconfirming | Medical | Safety |
| 8 | Hindi wording not reviewed by native speakers; Tamil message wording and the Tamil consent script not written | Language | Experience for Tamil families |
| 9 | Call cost recording needs a USD→INR rate set (`VOICE_COST_PAISE_PER_USD`) | Configuration | Dashboard cost accuracy |
| 10 | No automated alert phone call in emergencies (the spec allows one if the platform supports it); WhatsApp only for now | Feature | Faster emergency reach |
| 11 | No billing or payments; families are onboarded by hand | Feature | Fine for the pilot, needed later |
| 12 | Admin login is a simple username and password, with no two-factor login | Security | Fine for 2 founders; add 2FA before more admins |
| 13 | Vapi webhooks use a shared secret, not a signature (Vapi doesn't offer one) | Security | Needs HTTPS and careful secret handling |
| 14 | Data deletion at Vapi happens only after our retention period; Zero Data Retention not yet decided | Privacy | Decide with the lawyer |

---

## 6. Features we could add later

**Soon after the pilot**
- An automated emergency voice call to the family (not just WhatsApp).
- Tamil message templates and consent script; then more languages (Telugu, Bengali, Marathi, Kannada, Malayalam).
- A weekly health trend report for families (mood, sleep, pain over time).
- Medicine reminders at the actual dose time (short reminder calls).
- Family requests by WhatsApp ("ask Papa about his doctor visit tomorrow").
- Two-factor login for admins.

**Medium term**
- A simple family web or mobile app: history, trends, settings, call schedule.
- Self-service sign-up and online payment (UPI and cards) with subscription plans.
- An inbound line: the parent can call HaalChaal any time to talk or ask for help.
- Doctor and caregiver sharing (with consent), and appointment reminders.
- Smarter detection of slow changes (memory decline, loneliness, weight loss signs) for "watch" alerts.
- A WhatsApp voice-note option for parents who prefer it.

**Long term**
- Partnerships with hospitals, insurers, elder-care homes and residents' welfare associations.
- Integration with fall-detection devices or smart speakers for families who want them.
- Analytics for care partners (with consent and anonymisation).

---

## 7. Future plan, with requirements for each step

### Step 1: First real call to a founder (about 1–2 weeks)
**Goal:** the AI calls a founder's phone and a correct WhatsApp summary arrives within 5 minutes (Milestone 8).

| Requirement | Type | Who |
| --- | --- | --- |
| Vapi account (paid plan) and API key | Account and money | Founder |
| Indian phone number connected to Vapi (through a SIP trunk or telephony provider, e.g. Exotel, Plivo or Twilio India) with KYC documents | Account, documents and money | Founder |
| Set up the Vapi assistant, tools and webhook secret (guide: `docs/runbooks/vapi-setup.md`) | Setup | Founder, with my help |
| Test 2–3 voices and speech recognisers in Hindi, Tamil and English; pick the best | Decision | Founder and native speakers |
| Meta Business account, WhatsApp Business number, verified business, approved message templates | Account and approval | Founder |
| Anthropic API key (Claude) | Account and money | Founder |
| A public HTTPS address for webhooks: a staging server, or a temporary tunnel for testing | Technical | Me |
| Founders' phone numbers in the allowlist | Configuration | Me |
| Fix whatever the test calls reveal | Engineering | Me |

### Step 2: Put it online (about 1 week)
**Goal:** a live, secure system with backups (Milestone 9).

| Requirement | Type | Who |
| --- | --- | --- |
| Choose a hosting provider with an India region (e.g. AWS Mumbai, Google Cloud Mumbai, Azure India, DigitalOcean Bangalore) | Decision and money | Founder |
| Managed PostgreSQL with automatic backups | Infrastructure | Me |
| Domain name (e.g. `haalchaal.in`) and HTTPS certificate | Purchase and setup | Founder buys, I set up |
| Secrets storage (encryption keys, API keys) | Security | Me |
| Uptime and error alerts to founders' phones and email | Monitoring | Me |
| Test restoring a backup once | Operations | Me |
| Hardware | None. Cloud servers only; the founders need just a laptop and phone | — |

### Step 3: Legal, medical and language checks (in parallel with Steps 1–2)
**Goal:** permission and confidence to call real parents.

| Requirement | Type | Who |
| --- | --- | --- |
| Telecom compliance: automated calls registered as service calls (TRAI rules, DLT registration as needed); only then set `TELECOM_COMPLIANCE_ACK=true` | Law | Lawyer and founder |
| DPDP Act 2023: consent script (Hindi, English, Tamil), privacy notice, a guardian process when a parent can't consent, how data is exported and deleted | Law | Lawyer |
| Data retention periods for transcripts and recordings | Law and decision | Lawyer and founder |
| Permission to send call transcripts to AI services abroad (Anthropic, Vapi), or choose India-hosted options | Law | Lawyer |
| Data processing agreements with Vapi, Anthropic, Meta and the hosting provider | Contracts | Lawyer and founder |
| Review the red-flag list and alert timings | Medical | Doctor advisor |
| Confirm the helplines (112 emergency, Tele-MANAS 14416) | Safety | Founder |
| Review the Hindi wording; write and review the Tamil wording | Language | Native speakers, then me for Tamil |
| Terms of service and pricing for families | Business and law | Founder and lawyer |

### Step 4: Friendly trial (2–3 weeks)
**Goal:** prove it works with real parents who know us.

| Requirement | Type | Who |
| --- | --- | --- |
| 3–5 parents from family and friends, with written and recorded consent | People and consent | Founder |
| A founder checks every flagged call in the review queue each day | People (time) | Founders |
| A rota for urgent alerts (someone reachable 9 am–8 pm) | People | Founders |
| Feedback from parents and children (simple calls or a form) | Research | Founder |
| Fixes and improvements from what we learn | Engineering | Me |
| Budget for call minutes, AI and WhatsApp messages | Money | Founder |

### Step 5: Pilot (12 weeks)
**Goal:** 10–30 families, measured results, and a decision on what's next.

| Requirement | Type | Who |
| --- | --- | --- |
| 10–30 families recruited and onboarded in person | People and sales | Founders |
| Daily review queue and the alert rota continue | People | Founders (perhaps one helper) |
| Tracking of pickup rate, alerts answered and how fast, cost per family, family satisfaction | Metrics | Dashboard, plus me |
| Monthly security and safety check | Process | Me |
| Customer support line or WhatsApp for families | People | Founders |
| Decision at the end: pricing, scale-up, next features (Section 6) | Business | Founders |

### Summary of requirement types
- **Accounts and services:** Vapi, an Indian phone number or telephony provider, Meta WhatsApp Business, Anthropic, hosting, a domain.
- **People:**
  - founders for review and alerts, onboarding and support;
  - a lawyer;
  - a doctor advisor;
  - native Hindi and Tamil speakers;
  - trial and pilot families.
- **Law and approvals:** telecom service-call compliance (TRAI/DLT), the DPDP Act 2023 (consent, privacy notice, retention, cross-border data), Meta template approval, vendor agreements.
- **Money (monthly running costs):**
  - call minutes (Vapi plus telephony);
  - AI usage;
  - WhatsApp messages;
  - hosting, database and domain.
- **Hardware:** nothing special. A laptop for the founders and a phone for testing; everything else runs in the cloud. Parents need only any phone.

---

## 8. How to run it on your PC (quick reminder)

1. Start **Docker Desktop**.
2. In PowerShell, in the project folder, run `& "$env:TEMP\hc-run.ps1"` and keep the window open.
3. Open **http://localhost:8080/admin/** in your browser. The login details are in your notes, not in this file, because passwords must never go into GitHub.
4. Optional:
   - `make seed-demo` adds the demo families;
   - `make simcall SCENARIO=fall_emergency` runs a full simulated call.

Nothing real is called or messaged on your PC.

---

## 9. Conclusion

HaalChaal turns a simple daily phone call into a safety net for elderly parents: a friendly voice in their own language, a memory of how they have been, quick help when something goes wrong, and peace of mind for children far away. The hard engineering is done. Calls, safety rules, alerts, reports, memory, the admin website, privacy and security are all built and tested, and the connections to the real services are ready.

What stands between us and real families is no longer code. It's accounts, approvals and people:
1. a Vapi account and Indian number;
2. WhatsApp approval;
3. hosting;
4. legal and medical sign-off;
5. a few trusted families for the first trial.

With those in place, we can make the first real call within days and start the pilot soon after, carefully, safely and with every rule that protects elderly people kept in place.
