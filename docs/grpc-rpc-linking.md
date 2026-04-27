# gRPC / protobuf RPC linking across languages

This document records how `implements_rpc` edges link generated code back to proto `rpc`
declarations, and which TS/JS code-generators are handled vs. known gaps.

## Mechanism

`buildImplementsRPCEdges` (see `internal/indexer/implements_rpc.go`) probes the store for
generated-code symbol nodes whose dotted path matches a fixed set of per-language derivations
of each proto rpc's `pkg.Svc.Method` path.

For JS/TS the challenge is the *stem-fallback*: because JS/TS modules have no package
clause, the code handler assigns each symbol a path prefix equal to the file's basename
minus its final extension (e.g., `auth_grpc_web_pb.ts` → prefix `auth_grpc_web_pb`).
The resolver uses the proto `package` name (e.g., `auth`) as the stem. Generators that
decorate the output filename with a suffix (e.g., `_grpc_web_pb`) introduce a mismatch
between the two — this is *file-stem drift*.

## Generator survey (lib-r4s.10)

| Generator | buf.gen.yaml identity | Service file pattern | Symbol shape | Links today? | Note |
|---|---|---|---|---|---|
| `@bufbuild/protoc-gen-es` | `buf.build/bufbuild/es` | `*_pb.ts` (messages only) | — | n/a | No service symbols in `*_pb.ts` |
| `@connectrpc/protoc-gen-connect-es` | `buf.build/connectrpc/es` | `*_connect.ts` | `create*Client()` object literal | ✅ | Handled by `connectes` handler via `typeName`; stem-based matching not used |
| `ts-proto` | `ts-proto` / `protoc-gen-ts-proto` | `{file_stem}.ts` | `SvcClient` interface + `SvcClientImpl` class | ✅ (simple) / ⚠️ (multi-segment pkg) | File stem mirrors proto file name; single-segment packages (e.g., `auth.proto` → `package auth`) match. Multi-segment packages drift but this is the generic stem-fallback limitation, not a suffix issue |
| `protoc-gen-grpc-web` (Google) | `grpc-web` / `protoc-gen-grpc-web` | `{file_stem}_grpc_web_pb.js` + `.d.ts` | `SvcClient` class, method `lcMethod` | ✅ (fixed in lib-r4s.10) | Stem `auth_grpc_web_pb` ≠ proto pkg `auth`; resolver now probes `pkg_grpc_web_pb.*` candidates |
| `@improbable-eng/grpc-web` (`ts-protoc-gen`) | local plugin | `{file_stem}_pb_service.ts` | `SvcClient` class, method `lcMethod` | ✅ (fixed in lib-r4s.10) | Stem `auth_pb_service` ≠ proto pkg `auth`; resolver now probes `pkg_pb_service.*` candidates |

## Fix (lib-r4s.10): Option B — resolver-layer alternate candidates

For each broken generator the resolver now emits additional candidates using the known
decorated stem:

```
auth_grpc_web_pb.AuthServiceClient.login   ← protoc-gen-grpc-web
auth_pb_service.AuthServiceClient.login    ← @improbable-eng/grpc-web (ts-protoc-gen)
```

This approach:
- Does not affect message/enum symbol paths in search or the graph (only the resolver candidate probes are widened).
- Keeps the suffix list closed and auditable in one place (`implements_rpc.go`).
- Remains guarded by `candidateWithinCodegenTree` when a `buf.gen.yaml` manifest is present, so false positives are bounded by plugin output prefixes.

## Known gaps / non-scope

- **Multi-segment proto packages with ts-proto** — when `package com.example.auth` and the
  file stem is `auth`, the stem drift is a consequence of the generic JS/TS stem-fallback, not
  a generator-specific suffix. Fixing it would require either package-aware stem normalisation
  or a query-time package→file lookup. Out of scope for lib-r4s.10.
- **Hand-written TS implementations** — only link by coincidence if the file stem happens to
  equal the proto package name. Intentional: a generic "ignore filename decorations" rule
  would emit false edges.
- **Dart/Java/Python** — Dart uses `library` clauses, Java uses `package` clauses, Python has
  a custom `PackageName` heuristic; none use the stem-fallback, so this class of issue doesn't
  apply to them.
