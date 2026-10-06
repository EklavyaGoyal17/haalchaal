# Data processors

Every vendor that receives personal data, and exactly what it receives. Update this file before enabling any new vendor (SPEC §13). Real vendors are chosen in Milestone 8; until then all three are fakes that never leave the process.

| Vendor (role) | Status | Receives | Never receives |
| --- | --- | --- | --- |
| Voice platform (places calls, speech, transcripts) | Not chosen (fake in use) | Parent's phone number; rendered agent prompt (preferred name, family first names, medicine names and timings, up to 3 follow-ups, up to 3 interests, optional family code word, next call time); the call audio and transcript it produces | Family phone numbers, reports, private notes from earlier calls, other parents' data |
| LLM (post-call extraction) | Not chosen (fake in use) | One call's transcript, the parent's medicine names, active follow-ups, family summary language | Phone numbers, names of family members, earlier reports, other parents' data |
| WhatsApp (Meta Cloud API or a BSP) | Adapter ready, not enabled | Family members', local contacts' and admins' phone numbers; template parameters: parent's preferred name, the family summary (no private notes), alert reasons; red-flag and scam quotes only to family members | Transcripts, reports, medicine lists beyond what a summary says, private notes |
| Hosting and managed Postgres | Not chosen (M9; India region) | Everything at rest, with `_enc` columns encrypted by our keys | Encryption keys (kept in the environment, later a KMS) |
