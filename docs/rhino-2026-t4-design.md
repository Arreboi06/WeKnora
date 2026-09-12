# Rhino-bird 2026 Topic 4: Wiki source evidence profile

This note records the design intent and the reproducible verification boundary for the Topic 4 delivery. It is deliberately short: the implementation and tests remain the source of truth.

## Problem and design target

Wiki pages are useful only when an answer can be traced back to the source material that produced it. The implementation therefore treats a citation profile as a versioned projection of a source universe, rather than as an independently editable cache. A source change must either produce a new, complete projection or make the previous projection unusable.

## Invariants implemented in the submitted snapshot

1. **Complete source-universe invalidation.** Adding a Wiki page, changing its `SourceRefs`, or resolving a source set to empty invalidates the complete dependent profile. There is no backfill from an incomplete source set; old cursors and `read_version` values cannot continue reading an invalid projection.
2. **No stale concurrent publication.** Concurrent resolvers carry the source-universe version through the transaction and publication fence. A slower resolver cannot publish a result after a newer source mutation has invalidated its read.
3. **Fail-closed startup.** A failed or unrecovered dirty migration returns an error, closes the database handle, and prevents the application from serving traffic. The `AUTO_RECOVER_DIRTY` default is unchanged.
4. **Deterministic multi-scope locking.** Scope identifiers are normalised and acquired in a stable order, preventing lock-order inversion when one operation touches several scopes.
5. **Authoritative ACL synchronisation.** ACL transitions use a durable generation/marker and lease protocol. Revocation fences the old result, disabled peers cannot publish a stale `CURRENT` result, and reads require a current server-derived binding.
6. **Dialect parity.** SQLite and PostgreSQL use equivalent index semantics and drift checks so that the local test path does not silently diverge from the production database path.

## Verification path

The delivery was exercised with unit and package tests, Go formatting/vet checks, frontend checks, real PostgreSQL API/concurrency tests, restart and isolated backup/restore, and a real application + database + browser walkthrough. The relevant implementation and test entry points are discoverable in:

- [`internal/application/repository/citation_profile.go`](../internal/application/repository/citation_profile.go)
- [`internal/application/repository/citation_profile_acl.go`](../internal/application/repository/citation_profile_acl.go)
- [`internal/application/repository/citation_profile_pg_f1_remaining_test.go`](../internal/application/repository/citation_profile_pg_f1_remaining_test.go)
- [`internal/application/repository/citation_profile_pg_source_universe_race_test.go`](../internal/application/repository/citation_profile_pg_source_universe_race_test.go)
- [`internal/application/repository/citation_profile_acl_lock_order_f7_test.go`](../internal/application/repository/citation_profile_acl_lock_order_f7_test.go)
- [`internal/container/migration_startup_test.go`](../internal/container/migration_startup_test.go)
- [`migrations/versioned`](../migrations/versioned)

The submitted implementation snapshot is pinned at [`rhino-2026-final-T4`](https://github.com/Arreboi06/WeKnora/tree/rhino-2026-final-T4), commit `0bbb4853cb4bab38e1e947a1ec6b9d0178b40794`.

## Follow-up plan

The next confidence step is measurement, not a semantic change: collect two independent human annotation sheets for the Topic 4 effectiveness cases and adjudicate disagreements. A second environment with the required external Go module payloads should also repeat the CGO=0 container path. These follow-ups are kept separate from the submitted behaviour so that the reported result remains reproducible and auditable.
