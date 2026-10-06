# Encryption key rotation

Columns ending in `_enc` are AES-256-GCM with a one-byte key id prefix, so
old and new keys can coexist.

1. Generate a new key: `make genkey KID=2` (key ids are 1 to 255).
2. Append it to `ENCRYPTION_KEYS` everywhere (`1:<old>,2:<new>`), keep `ENCRYPTION_ACTIVE_KID=1`, and deploy. Every instance can now read both.
3. Set `ENCRYPTION_ACTIVE_KID=2` and deploy. New writes use key 2. Admin form tokens (CSRF) issued under key 1 expire; admins reload the page.
4. Run `haalchaal rotate-keys` as a one-off task. It re-encrypts every value still under key 1, in pages, with compare-and-set updates, and writes an audit row. It is safe to stop and re-run; a second run that reports 0 values means rotation is complete.
5. Remove key 1 from `ENCRYPTION_KEYS` and deploy. Keep a sealed copy of key 1 until every backup taken before step 4 has expired.

If a key is suspected compromised, do steps 1 to 5 the same day, and treat it as a possible breach (`breach.md`).
