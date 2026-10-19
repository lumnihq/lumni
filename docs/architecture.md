# Runtime architecture

Lumni is a modular Go monolith with separate process entry points and separate PostgreSQL pools for admission, ingestion, query, and background work.

```text
Agent / MCP / Runtime
          |
          v
 Gateway or SDK ---- OTLP / GitHub / Events
          |                    |
          v                    v
      HTTP API            soft ingestion
          |
          v
 deterministic kernel
          |
          v
 PostgreSQL transaction ---- transactional outbox
          |                         |
          v                         v
 existing runtime              worker/webhooks
```

The admission transaction normalizes identity, locks idempotency ownership, checks committed/active effects, compares opaque versions within one source, detects active write conflicts, evaluates typed policy, locks write resources in stable order, increments fences, acquires all leases, and stores its receipt and outbox event atomically.

Shadow decisions never acquire leases. Semantic models, Git enrichment, OTLP, graphs, rollups, and delivery workers do not participate in the hard decision path.

PostgreSQL remains the graph source: work/action/resource edges are ordinary tenant-scoped relationships. A graph database, vector store, Redis, Kafka, or global lock service is not required by P0/P1.
