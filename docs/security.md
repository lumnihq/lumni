# Security model

- Terminate TLS at a trusted customer-controlled ingress.
- API keys contain 256 random bits, are returned once, and are stored as SHA-256 hashes with only a visible prefix.
- Scopes are `admin`, `ingest`, `admission`, and `query`; all database access includes tenant context.
- Webhook signing secrets are encrypted with AES-256-GCM using `LUMNI_SECRET_ENCRYPTION_KEY` and are never returned by list APIs.
- GitHub deliveries require `X-Hub-Signature-256` and constant-time HMAC validation, then deduplicate on `X-GitHub-Delivery`.
- Raw action arguments and results are optional. Production deployments should disable or redact them at the gateway when they contain sensitive data.
- Logs contain IDs, decisions, reason codes, and algorithm versions—not authorization headers, API keys, webhook secrets, or raw tool payloads.

The local Compose credentials are intentionally public development defaults. Replace the database password, bootstrap API key, encryption key, and GitHub secret before exposing a deployment.
