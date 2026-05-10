# Graph Report - .  (2026-05-10)

## Corpus Check
- cluster-only mode — file stats not available

## Summary
- 701 nodes · 1418 edges · 34 communities (22 shown, 12 thin omitted)
- Extraction: 90% EXTRACTED · 10% INFERRED · 0% AMBIGUOUS · INFERRED: 137 edges (avg confidence: 0.81)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `151b5639`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- [[_COMMUNITY_Community 0|Community 0]]
- [[_COMMUNITY_Community 1|Community 1]]
- [[_COMMUNITY_Community 2|Community 2]]
- [[_COMMUNITY_Community 3|Community 3]]
- [[_COMMUNITY_Community 4|Community 4]]
- [[_COMMUNITY_Community 5|Community 5]]
- [[_COMMUNITY_Community 6|Community 6]]
- [[_COMMUNITY_Community 7|Community 7]]
- [[_COMMUNITY_Community 8|Community 8]]
- [[_COMMUNITY_Community 9|Community 9]]
- [[_COMMUNITY_Community 10|Community 10]]
- [[_COMMUNITY_Community 11|Community 11]]
- [[_COMMUNITY_Community 12|Community 12]]
- [[_COMMUNITY_Community 13|Community 13]]
- [[_COMMUNITY_Community 14|Community 14]]
- [[_COMMUNITY_Community 15|Community 15]]
- [[_COMMUNITY_Community 16|Community 16]]
- [[_COMMUNITY_Community 17|Community 17]]
- [[_COMMUNITY_Community 18|Community 18]]
- [[_COMMUNITY_Community 22|Community 22]]
- [[_COMMUNITY_Community 23|Community 23]]
- [[_COMMUNITY_Community 24|Community 24]]
- [[_COMMUNITY_Community 25|Community 25]]
- [[_COMMUNITY_Community 33|Community 33]]

## God Nodes (most connected - your core abstractions)
1. `HandlerSuite` - 98 edges
2. `ManagerTestSuite` - 47 edges
3. `newTestNote()` - 31 edges
4. `RepositoryTestSuite` - 28 edges
5. `ManagerSuite` - 24 edges
6. `newNote()` - 18 edges
7. `OgenHandler` - 11 edges
8. `newFailWriter()` - 11 edges
9. `RendererSuite` - 10 edges
10. `MockStorage` - 10 edges

## Surprising Connections (you probably didn't know these)
- `Padmark OpenAPI 3.1 Specification` --semantically_similar_to--> `Internal Copy of Padmark OpenAPI Spec (served at /api/openapi.yaml)`  [INFERRED] [semantically similar]
  openapi.yaml → internal/adapters/http/spec/openapi.yaml
- `Private Note Flag Feature` --implements--> `HTML Template: Note View Page`  [EXTRACTED]
  migrations/postgres/20260430163000_add_private.sql → internal/adapters/http/templates/note.html
- `Postgres Init Migration (notes table)` --references--> `Docker Compose (PostgreSQL + migrate + app)`  [INFERRED]
  migrations/postgres/20260329000001_init.sql → docker-compose.yml
- `Notes Table Schema` --shares_data_with--> `Docker Compose (PostgreSQL + migrate + app)`  [INFERRED]
  migrations/postgres/20260329000001_init.sql → docker-compose.yml
- `Notes Table Schema` --shares_data_with--> `Padmark OpenAPI 3.1 Specification`  [INFERRED]
  migrations/postgres/20260329000001_init.sql → openapi.yaml

## Communities (34 total, 12 thin omitted)

### Community 0 - "Community 0"
Cohesion: 0.08
Nodes (43): CreateNoteRequest, CreateNoteRequestContentType, CreateNoteRes, CreateNoteResponse, DeleteNoteNoContent, DeleteNoteRes, ErrorResponse, GetNoteRes (+35 more)

### Community 1 - "Community 1"
Cohesion: 0.05
Nodes (52): BurnAfterReadingPolicy, StorageDIP, ContentType, SentinelErrors, Note, TestContentType_Valid(), Note.Validate, NewMockNoteManager() (+44 more)

### Community 2 - "Community 2"
Cohesion: 0.05
Nodes (41): DeleteNoteParams, GetNoteParams, Handler, NewServer, Server, UpdateNoteParams, Handler.APIDocsPage, domainErrToPageData (+33 more)

### Community 3 - "Community 3"
Cohesion: 0.06
Nodes (44): Client, Invoker, NewClient, ReadyzOK, trimTrailingSlashes(), serverURLKey, UnimplementedHandler, domainToCreateResponse (+36 more)

### Community 4 - "Community 4"
Cohesion: 0.06
Nodes (37): OperationName, globalFlags(), newPadmarkClient(), noteIDArg(), readContent(), bearerTransport, createAction, buildCreateReq() (+29 more)

### Community 7 - "Community 7"
Cohesion: 0.1
Nodes (17): ClientOption, Option, ServerOption, baseClient, baseServer, clientConfig, notAllowedParams, newClientConfig() (+9 more)

### Community 8 - "Community 8"
Cohesion: 0.11
Nodes (23): postgres.Migrate, buildRouter, initPostgresStorage(), initSQLiteStorage(), logMigrations(), openPostgresDB(), dbOpener, initStorage (+15 more)

### Community 9 - "Community 9"
Cohesion: 0.1
Nodes (10): contextKey, makeTokenSet(), NewRouter, RouterOptions, withBodyLimit, withLogging, withRecovery, withRequestID (+2 more)

### Community 12 - "Community 12"
Cohesion: 0.14
Nodes (25): Burn-After-Reading Pattern (self-destruct notes), Project CLAUDE.md (AI Instructions), Clean Architecture Pattern (domain/usecases/infra/adapters), Content Negotiation Pattern (HTML/JSON/Markdown), Docker Compose (PostgreSQL + migrate + app), Go Service Development Guide (GUIDE.md), Edit Code Pattern (one-time secret for PUT/DELETE), Partial Index on expires_at for TTL Expiry (+17 more)

### Community 13 - "Community 13"
Cohesion: 0.11
Nodes (13): APISpec, codeRecorder, apiDocsViewData, errorWriter, failWriter, TestAPISpec_OK_SetsContentType(), TestAPISpec_WriteError_DoesNotCorruptBody(), TestIsPublicRoute() (+5 more)

### Community 15 - "Community 15"
Cohesion: 0.17
Nodes (4): newFailWriter(), testNoteResponse, NewHandler, NewOgenHandler

### Community 17 - "Community 17"
Cohesion: 0.22
Nodes (3): PostgresManagerSuite, SQLiteManagerSuite, FS (sqlitemigrations)

## Knowledge Gaps
- **13 isolated node(s):** `serverURLKey`, `notAllowedParams`, `labelerContextKey`, `testNoteResponse`, `noteJSON` (+8 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **12 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `HandlerSuite` connect `Community 10` to `Community 1`, `Community 5`, `Community 6`, `Community 9`, `Community 11`, `Community 13`, `Community 14`, `Community 15`, `Community 17`?**
  _High betweenness centrality (0.194) - this node is a cross-community bridge._
- **Why does `ManagerTestSuite` connect `Community 6` to `Community 1`, `Community 5`, `Community 11`, `Community 14`, `Community 17`?**
  _High betweenness centrality (0.097) - this node is a cross-community bridge._
- **Why does `OgenHandler` connect `Community 3` to `Community 0`, `Community 9`, `Community 1`, `Community 15`?**
  _High betweenness centrality (0.089) - this node is a cross-community bridge._
- **Are the 7 inferred relationships involving `NewRouter` (e.g. with `NewHandler` and `NewOgenHandler`) actually correct?**
  _`NewRouter` has 7 INFERRED edges - model-reasoned connections that need verification._
- **What connects `serverURLKey`, `notAllowedParams`, `labelerContextKey` to the rest of the system?**
  _13 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `Community 0` be split into smaller, more focused modules?**
  _Cohesion score 0.08 - nodes in this community are weakly interconnected._
- **Should `Community 1` be split into smaller, more focused modules?**
  _Cohesion score 0.05 - nodes in this community are weakly interconnected._