# Graph Report - .  (2026-05-10)

## Corpus Check
- 116 files · ~75,637 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 1570 nodes · 2109 edges · 138 communities (59 shown, 79 thin omitted)
- Extraction: 94% EXTRACTED · 6% INFERRED · 0% AMBIGUOUS · INFERRED: 121 edges (avg confidence: 0.81)
- Token cost: 0 input · 0 output

## Community Hubs (Navigation)
- [[_COMMUNITY_Server Infrastructure|Server Infrastructure]]
- [[_COMMUNITY_Notes Manager Tests|Notes Manager Tests]]
- [[_COMMUNITY_HTTP Handler & Auth|HTTP Handler & Auth]]
- [[_COMMUNITY_Domain Model & Error Mapping|Domain Model & Error Mapping]]
- [[_COMMUNITY_CLI Application|CLI Application]]
- [[_COMMUNITY_Markdown Renderer & Integration Tests|Markdown Renderer & Integration Tests]]
- [[_COMMUNITY_HTTP Handler Test Suite|HTTP Handler Test Suite]]
- [[_COMMUNITY_HTTP Auth & Misc Tests|HTTP Auth & Misc Tests]]
- [[_COMMUNITY_OGen Generated Server|OGen Generated Server]]
- [[_COMMUNITY_Client Generated Code|Client Generated Code]]
- [[_COMMUNITY_Project Docs & Design Patterns|Project Docs & Design Patterns]]
- [[_COMMUNITY_HTTP Handler Mocks|HTTP Handler Mocks]]
- [[_COMMUNITY_OGen NoteResponse Type|OGen NoteResponse Type]]
- [[_COMMUNITY_OGen CreateNoteResponse Type|OGen CreateNoteResponse Type]]
- [[_COMMUNITY_SQLite Repository Tests|SQLite Repository Tests]]
- [[_COMMUNITY_Client CreateNoteResponse|Client CreateNoteResponse]]
- [[_COMMUNITY_Client NoteResponse|Client NoteResponse]]
- [[_COMMUNITY_Notes Manager Mocks|Notes Manager Mocks]]
- [[_COMMUNITY_Client Optional DateTime|Client Optional DateTime]]
- [[_COMMUNITY_Client Optional Types|Client Optional Types]]
- [[_COMMUNITY_Component 20|Component 20]]
- [[_COMMUNITY_Component 21|Component 21]]
- [[_COMMUNITY_Component 22|Component 22]]
- [[_COMMUNITY_Component 23|Component 23]]
- [[_COMMUNITY_Component 24|Component 24]]
- [[_COMMUNITY_Component 25|Component 25]]
- [[_COMMUNITY_Component 26|Component 26]]
- [[_COMMUNITY_Component 27|Component 27]]
- [[_COMMUNITY_Component 28|Component 28]]
- [[_COMMUNITY_Component 29|Component 29]]
- [[_COMMUNITY_Component 30|Component 30]]
- [[_COMMUNITY_Component 31|Component 31]]
- [[_COMMUNITY_Component 32|Component 32]]
- [[_COMMUNITY_Component 33|Component 33]]
- [[_COMMUNITY_Component 34|Component 34]]
- [[_COMMUNITY_Component 35|Component 35]]
- [[_COMMUNITY_Component 36|Component 36]]
- [[_COMMUNITY_Component 37|Component 37]]
- [[_COMMUNITY_Component 38|Component 38]]
- [[_COMMUNITY_Component 39|Component 39]]
- [[_COMMUNITY_Component 40|Component 40]]
- [[_COMMUNITY_Component 41|Component 41]]
- [[_COMMUNITY_Component 42|Component 42]]
- [[_COMMUNITY_Component 43|Component 43]]
- [[_COMMUNITY_Component 44|Component 44]]
- [[_COMMUNITY_Component 45|Component 45]]
- [[_COMMUNITY_Component 46|Component 46]]
- [[_COMMUNITY_Component 47|Component 47]]
- [[_COMMUNITY_Component 48|Component 48]]
- [[_COMMUNITY_Component 49|Component 49]]
- [[_COMMUNITY_Component 50|Component 50]]
- [[_COMMUNITY_Component 51|Component 51]]
- [[_COMMUNITY_Component 52|Component 52]]
- [[_COMMUNITY_Component 53|Component 53]]
- [[_COMMUNITY_Component 54|Component 54]]
- [[_COMMUNITY_Component 55|Component 55]]
- [[_COMMUNITY_Component 56|Component 56]]
- [[_COMMUNITY_Component 57|Component 57]]
- [[_COMMUNITY_Component 58|Component 58]]
- [[_COMMUNITY_Component 59|Component 59]]
- [[_COMMUNITY_Component 60|Component 60]]
- [[_COMMUNITY_Component 61|Component 61]]
- [[_COMMUNITY_Component 62|Component 62]]
- [[_COMMUNITY_Component 63|Component 63]]
- [[_COMMUNITY_Component 64|Component 64]]
- [[_COMMUNITY_Component 65|Component 65]]
- [[_COMMUNITY_Component 66|Component 66]]
- [[_COMMUNITY_Component 69|Component 69]]
- [[_COMMUNITY_Component 72|Component 72]]
- [[_COMMUNITY_Component 73|Component 73]]
- [[_COMMUNITY_Component 75|Component 75]]
- [[_COMMUNITY_Component 76|Component 76]]
- [[_COMMUNITY_Component 77|Component 77]]
- [[_COMMUNITY_Component 78|Component 78]]
- [[_COMMUNITY_Component 79|Component 79]]
- [[_COMMUNITY_Component 80|Component 80]]
- [[_COMMUNITY_Component 81|Component 81]]
- [[_COMMUNITY_Component 82|Component 82]]
- [[_COMMUNITY_Component 83|Component 83]]
- [[_COMMUNITY_Component 84|Component 84]]
- [[_COMMUNITY_Component 85|Component 85]]
- [[_COMMUNITY_Component 86|Component 86]]
- [[_COMMUNITY_Component 87|Component 87]]
- [[_COMMUNITY_Component 88|Component 88]]
- [[_COMMUNITY_Component 89|Component 89]]
- [[_COMMUNITY_Component 90|Component 90]]
- [[_COMMUNITY_Component 91|Component 91]]
- [[_COMMUNITY_Component 92|Component 92]]
- [[_COMMUNITY_Component 93|Component 93]]
- [[_COMMUNITY_Component 94|Component 94]]
- [[_COMMUNITY_Component 95|Component 95]]
- [[_COMMUNITY_Component 96|Component 96]]
- [[_COMMUNITY_Component 97|Component 97]]
- [[_COMMUNITY_Component 98|Component 98]]
- [[_COMMUNITY_Component 99|Component 99]]
- [[_COMMUNITY_Component 100|Component 100]]
- [[_COMMUNITY_Component 101|Component 101]]
- [[_COMMUNITY_Component 102|Component 102]]
- [[_COMMUNITY_Component 104|Component 104]]
- [[_COMMUNITY_Component 105|Component 105]]
- [[_COMMUNITY_Component 106|Component 106]]
- [[_COMMUNITY_Component 107|Component 107]]
- [[_COMMUNITY_Component 108|Component 108]]
- [[_COMMUNITY_Component 110|Component 110]]
- [[_COMMUNITY_Component 112|Component 112]]
- [[_COMMUNITY_Component 113|Component 113]]
- [[_COMMUNITY_Component 114|Component 114]]
- [[_COMMUNITY_Component 137|Component 137]]

## God Nodes (most connected - your core abstractions)
1. `HandlerSuite` - 100 edges
2. `ManagerTestSuite` - 52 edges
3. `newTestNote()` - 33 edges
4. `CreateNoteResponse` - 29 edges
5. `NoteResponse` - 29 edges
6. `CreateNoteResponse` - 29 edges
7. `NoteResponse` - 29 edges
8. `RepositoryTestSuite` - 27 edges
9. `RepositoryTestSuite` - 25 edges
10. `CreateNoteRequest` - 24 edges

## Surprising Connections (you probably didn't know these)
- `Private Note Flag Feature` --conceptually_related_to--> `HTML Template: Login Page (token auth)`  [INFERRED]
  migrations/postgres/20260430163000_add_private.sql → internal/adapters/http/templates/login.html
- `Padmark OpenAPI 3.1 Specification` --semantically_similar_to--> `Internal Copy of Padmark OpenAPI Spec (served at /api/openapi.yaml)`  [INFERRED] [semantically similar]
  openapi.yaml → internal/adapters/http/spec/openapi.yaml
- `Postgres Init Migration (notes table)` --references--> `Docker Compose (PostgreSQL + migrate + app)`  [INFERRED]
  migrations/postgres/20260329000001_init.sql → docker-compose.yml
- `Notes Table Schema` --shares_data_with--> `Padmark OpenAPI 3.1 Specification`  [INFERRED]
  migrations/postgres/20260329000001_init.sql → openapi.yaml
- `Notes Table Schema` --shares_data_with--> `Docker Compose (PostgreSQL + migrate + app)`  [INFERRED]
  migrations/postgres/20260329000001_init.sql → docker-compose.yml

## Communities (138 total, 79 thin omitted)

### Community 0 - "Server Infrastructure"
Cohesion: 0.05
Nodes (27): PostgresManagerSuite, newNote(), RepositoryTestSuite, NewApp(), initPostgresStorage(), initSQLiteStorage(), initStorage(), logMigrations() (+19 more)

### Community 2 - "HTTP Handler & Auth"
Cohesion: 0.06
Nodes (29): authMiddleware, contextKey, writeError(), errorViewData, Handler, extractToken(), isPublicPath(), isPublicRoute() (+21 more)

### Community 3 - "Domain Model & Error Mapping"
Cohesion: 0.07
Nodes (21): ContentType, Note, TestContentType_Valid(), errResp(), mapCreateError(), mapDeleteError(), mapGetError(), mapUpdateError() (+13 more)

### Community 4 - "CLI Application"
Cohesion: 0.08
Nodes (31): globalFlags(), NewApp(), newPadmarkClient(), noteIDArg(), readContent(), bearerTransport, buildCreateReq(), createAction() (+23 more)

### Community 5 - "Markdown Renderer & Integration Tests"
Cohesion: 0.05
Nodes (5): ManagerSuite, newManager(), NewRenderer(), Renderer, RendererSuite

### Community 8 - "OGen Generated Server"
Cohesion: 0.08
Nodes (16): baseClient, baseServer, clientConfig, ClientOption, notAllowedParams, newClientConfig(), newServerConfig(), WithAttributes() (+8 more)

### Community 9 - "Client Generated Code"
Cohesion: 0.08
Nodes (16): baseClient, baseServer, clientConfig, ClientOption, notAllowedParams, newClientConfig(), newServerConfig(), WithAttributes() (+8 more)

### Community 10 - "Project Docs & Design Patterns"
Cohesion: 0.12
Nodes (29): Burn-After-Reading Pattern (self-destruct notes), Project CLAUDE.md (AI Instructions), Clean Architecture Pattern (domain/usecases/infra/adapters), Content Negotiation Pattern (HTML/JSON/Markdown), Docker Compose (PostgreSQL + migrate + app), Go Service Development Guide (GUIDE.md), Edit Code Pattern (one-time secret for PUT/DELETE), Partial Index on expires_at for TTL Expiry (+21 more)

### Community 11 - "HTTP Handler Mocks"
Cohesion: 0.07
Nodes (6): NewMockNoteManager(), NewMockPinger(), MockNoteManager, MockNoteManagerMockRecorder, MockPinger, MockPingerMockRecorder

### Community 17 - "Notes Manager Mocks"
Cohesion: 0.08
Nodes (6): NewMockRenderer(), NewMockStorage(), MockRenderer, MockRendererMockRecorder, MockStorage, MockStorageMockRecorder

### Community 18 - "Client Optional DateTime"
Cohesion: 0.09
Nodes (3): OptNilDateTime, format, OptNilDateTime

### Community 22 - "Component 22"
Cohesion: 0.18
Nodes (5): Manager, newEditCode(), newSlug(), Renderer, Storage

### Community 23 - "Component 23"
Cohesion: 0.17
Nodes (5): Client, Invoker, NewClient(), trimTrailingSlashes(), serverURLKey

### Community 24 - "Component 24"
Cohesion: 0.17
Nodes (5): Client, Invoker, NewClient(), trimTrailingSlashes(), serverURLKey

### Community 25 - "Component 25"
Cohesion: 0.12
Nodes (3): CreateNoteBadRequest, CreateNoteRequestEntityTooLarge, UpdateNoteUnauthorized

### Community 26 - "Component 26"
Cohesion: 0.12
Nodes (3): DeleteNoteInternalServerError, DeleteNoteUnauthorized, UpdateNoteForbidden

### Community 29 - "Component 29"
Cohesion: 0.26
Nodes (14): clientIP(), isTrustedProxy(), newReq(), parseCIDR(), TestClientIP_MalformedRemoteAddr(), TestClientIP_NoProxies_UsesRemoteAddr(), TestClientIP_TrustedProxy_XForwardedFor(), TestClientIP_TrustedProxy_XRealIP() (+6 more)

### Community 30 - "Component 30"
Cohesion: 0.14
Nodes (3): DeleteNoteInternalServerError, GetNoteGone, UpdateNoteUnprocessableEntity

### Community 31 - "Component 31"
Cohesion: 0.14
Nodes (3): CreateNoteBadRequest, DeleteNoteNotFound, UpdateNoteUnprocessableEntity

### Community 32 - "Component 32"
Cohesion: 0.2
Nodes (5): NewHandler(), parseTmpl(), newFailWriter(), NoteManager, Pinger

### Community 36 - "Component 36"
Cohesion: 0.19
Nodes (6): note, Repository, boolVal(), contentTypePtr(), contentTypeVal(), toDomain()

### Community 39 - "Component 39"
Cohesion: 0.17
Nodes (3): DeleteNoteNoContent, HealthzOK, ReadyzOK

### Community 40 - "Component 40"
Cohesion: 0.17
Nodes (3): DeleteNoteNoContent, HealthzOK, ReadyzOK

### Community 42 - "Component 42"
Cohesion: 0.2
Nodes (3): DeleteNoteParams, GetNoteParams, UpdateNoteParams

### Community 43 - "Component 43"
Cohesion: 0.2
Nodes (3): DeleteNoteParams, GetNoteParams, UpdateNoteParams

### Community 44 - "Component 44"
Cohesion: 0.28
Nodes (4): errorWriter, TestAPISpec_OK_SetsContentType(), TestStatusRecorder_WriteHeader_DelegatesOnce(), spyResponseWriter

### Community 88 - "Component 88"
Cohesion: 0.33
Nodes (5): CreateNoteRes, DeleteNoteRes, GetNoteRes, ReadyzRes, UpdateNoteRes

### Community 102 - "Component 102"
Cohesion: 0.33
Nodes (5): CreateNoteRes, DeleteNoteRes, GetNoteRes, ReadyzRes, UpdateNoteRes

### Community 104 - "Component 104"
Cohesion: 0.4
Nodes (3): apiDocsViewData, TestAPISpec_WriteError_DoesNotCorruptBody(), APISpec()

## Knowledge Gaps
- **60 isolated node(s):** `successViewData`, `errorViewData`, `loginViewData`, `testNoteResponse`, `noteViewData` (+55 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **79 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `negotiate()` connect `HTTP Handler & Auth` to `Client Optional DateTime`?**
  _High betweenness centrality (0.236) - this node is a cross-community bridge._
- **Why does `OptNilDateTime` connect `Client Optional DateTime` to `Component 35`, `Component 39`?**
  _High betweenness centrality (0.234) - this node is a cross-community bridge._
- **What connects `successViewData`, `errorViewData`, `loginViewData` to the rest of the system?**
  _60 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `Server Infrastructure` be split into smaller, more focused modules?**
  _Cohesion score 0.05 - nodes in this community are weakly interconnected._
- **Should `Notes Manager Tests` be split into smaller, more focused modules?**
  _Cohesion score 0.04 - nodes in this community are weakly interconnected._
- **Should `HTTP Handler & Auth` be split into smaller, more focused modules?**
  _Cohesion score 0.06 - nodes in this community are weakly interconnected._
- **Should `Domain Model & Error Mapping` be split into smaller, more focused modules?**
  _Cohesion score 0.07 - nodes in this community are weakly interconnected._