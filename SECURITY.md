# Security

Report vulnerabilities privately to the repository maintainers. Do not include production secrets or customer payloads in an issue.

Lumni assumes TLS termination at a trusted ingress. API keys are tenant-scoped and stored only as SHA-256 hashes. Deployments must replace development credentials, restrict PostgreSQL access, and use least-privilege GitHub, verifier, and webhook credentials.
