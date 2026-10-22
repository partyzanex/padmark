# CODEREVIEW — ветка `feature/owned-notes` (REDTEAM + CODEREVIEW)

**Дата:** 2026-10-14 · **Базис:** `main` · **Ревьюируемое состояние:** рабочее дерево на коммите `b6d0ffc` (feat(links): add PUT /links/{id})
**Область:** ~21 000 строк ручного кода ветки (без сгенерированных `ogenapi/`, `pkg/client/`), включая ранее незакоммиченные файлы (`contentkeys`, `links/update.go`, `ogen_link_update.go`, `service_unavailable.go` и др.)

**Метод:** 5 параллельных ревью-доменов (auth/session/CSRF; crypto/keyring; HTTP-поверхность/шаблоны/спека; domain/usecases/storage/migrations; CLI/MCP/composition root) + прогон build/lint/test + **личный фактчекинг каждой находки по коду** до записи. Все находки ниже подтверждены чтением кода; неподтверждённые отброшены.

---

## Сводка

**Текущий статус, 2026-10-21 (после повторной верификации):** обязательные исправления
R-01–R-27a закрыты; Info-пункты исправлены либо имеют явно описанное
архитектурное/криптографическое ограничение (R-28, R-29, R-32, R-33, R-44). P-01/P-02 закрыты,
P-03 обоснован (описание исправлено). Повторная проверка 2026-10-21 выявила, что статус
2026-10-19 был завышен для R-01 и R-08 (остаточные дыры уровня Medium), а ряд фиксов внёс
побочные дефекты; всё это исправлено — см. раздел «Повторная верификация (2026-10-21)».
Ниже сохранены исходные severity/counts для истории аудита.

| Severity | Кол-во | Находки |
|---|---|---|
| **High** | 1 | R-01 |
| **Medium** | 7 | R-02 … R-08 |
| **Low** | 20 | R-09 … R-27, R-27a |
| **Info** | 17 | R-28 … R-44 |
| **Процесс/DoD** | 3 | P-01 … P-03 |

Исходные топ-3 риска (до исправлений): **R-01** (апгрейд БД с plaintext-эры = отказ старта без средства лечения), **R-02** (targeted DoS редактирования), **R-03** (готовые bearer-credentials при утечке БД).

---

## Повторная верификация (2026-10-21)

**Метод:** каждый пункт R-01…R-44, P-01…P-03 перепроверен по коду рабочего дерева (5 доменов,
независимо от исходного ревью), критичные находки — личным чтением кода. Найденные остаточные
дефекты исправлены в том же рабочем дереве с регрессионными тестами.

### Остаточные дефекты и их исправление

| ID | Severity | Связь | Суть | Исправление |
|---|---|---|---|---|
| N-01 | **Medium** | R-08 | `padmark-cli setup` брал URL из непроверенного `pmk_`-envelope (`resolveServerURL`) и слал пароль первого админа собственным `http.Client` без `ensureTrustedServer` — обход TOFU; утверждение «guard до любого запроса» было ложным | Единый гейт `resolveTrustedServer` (`cli/trust.go`) используется и `newPadmarkClient`, и `setupAction` до чтения stdin, warning-а и любого I/O. Аудит: других путей с собственными запросами в CLI/MCP нет. Тесты `TestSetup_UntrustedEnvelope_BlocksBeforeAnyRequest` (`--token`/env/tokenfile), `TestSetup_ExplicitURLOverridesUntrustedEnvelope`, `TestSetup_TrustedEnvelopeProceeds` |
| N-02 | **Medium** | R-01 | Plaintext-строка с `$` (цены, LaTeX, `$HOME`) считалась envelope → `Failed`, но не `CorruptRaw` → `serve`/`migrate` заблокированы, `prune --corrupt` отказывал целиком, а подсказка ошибки отправляла в тот же `prune` (замкнутый круг) | Единая синтаксическая классификация `crypto.IsVersionedContent` (через `Cipher.Versioned`): envelope ⇔ сегмент до первого `$` = `pmc<digits>`. Валидный legacy — канонический base64, `$` не содержит, поэтому всё прочее невалидное = corrupt raw (prunable). Versioned-значения, не прошедшие аутентификацию (чужой ключ, подмена, `pmc9$…`, `pmc2$garbage`), остаются `Failed`, никогда не удаляются и блокируют prune; подсказка в этом случае не предлагает prune. Используется в `inspect`, `Migrate`, `ConvertSlugs`, `PruneCorrupt`. Тесты `TestIsVersionedContent`, `TestCorruptRawWithDollarIsPrunable`, `TestMixedDatabaseClassification`, `TestInvalidEnvelopeCannotBePrunedOrBindNewRegistry`; интеграционный `testPruneCorruptRaw` (+строка с `$`), `requireUnprunableEnvelope` |
| N-03 | Medium (docs/UX) | R-35 | `--disable-api` ломает публикацию/сохранение из веб-редактора (`fetch POST /notes`, `PUT /notes/{id}` → 503), а usage и README утверждали «Web routes keep working»; usage не перечислял `/llms.txt`, `/api/tokens`, `/api/invites` | Usage (`server/flags.go`) и README перечисляют все блокируемые пути и прямо говорят: веб-UI становится read-only. Редактор показывает текст ошибки сервера («API is disabled») вместо безликого «Error» |
| N-04 | Low–Medium | R-34 | За TLS-прокси без `--trusted-proxies`/`--public-scheme` (или с переписыванием Host) новый `requireSameOrigin` отдаёт 403 на PUT/DELETE редактора, а JS показывал любой 403 как «Invalid edit code»; не задокументировано. Комментарий `router.go` утверждал, что same-origin покрывает все мутации, хотя POST `/notes`, `/links`, `/links/bulk` не обёрнуты | README (раздел про `--trusted-proxies`) описывает требование и последствия (включая то, что `--public-scheme` не делает cookie `Secure`). JS показывает сообщение сервера; «Invalid edit code» — только для 403 с этим текстом (`TestEditorMatchesEditCodeErrorMessage` держит синхронность с domain). Комментарии `registerOgenRoutes`/`requireSameOrigin` точно описывают охват и почему POST-создание опирается на JSON-preflight + SameSite |
| N-05 | Low | R-03 | Down миграции `20261014000001` возвращал имя колонки, но оставлял хэши — после отката старый бинарник принял бы утёкший хэш как cookie (invite-миграция этот случай закрывала) | `DELETE FROM sessions` в Down обоих backend-ов. Тест `TestMigrateSessionIDs_InvalidatesOldSessionsAndDowngradeHashes` (проверено, что без фикса падает) |
| N-06 | Low | R-12 (давний баг, есть и в `main`) | После смены пароля форма перерисовывалась со старым CSRF-токеном, хотя cookie уже ротирован → повторная отправка давала 403 | Ротированный токен прокидывается в view data. Тесты `TestChangePasswordHandler_RerenderCarriesRotatedCSRFToken` (успех и частичный успех), `TestChangePasswordHandler_FailureKeepsRequestCSRFToken`. Login/setup проверены — паттерна нет |
| N-07 | Low | R-22 | `fmt.Errorf("render note %s: %w", id, err)` с plaintext slug (`usecases/notes/manager.go`) — латентная утечка при будущем логировании | `domain.HashSlug(id)`; тесты `TestGetRendered_RenderError`, `TestGetRendered_PlainText_RenderPlainError` проверяют отсутствие slug |
| N-08 | Low | R-23 | При `burn_after_reading: true` `ttl: null` снимал существующий hard expiry, а omitted/`0` — нет; спека обещает их эквивалентность | `ClearExpiry` только при выключенном burn. Тесты `TestUpdateNoteBurnTTLNullEqualsOmittedAndZero` (оба вида slug), `TestUpdateBurnImmediateExpiry` |
| N-09 | Low | R-24 | Без `ps`-предупреждения остались `--dsn` (пароль Postgres), `--edit-code`, `--invite` | Usage дополнены (у `--invite` честно указано, что альтернативы флагу нет); README-таблица для `--dsn` |
| N-10 | Low | R-07 | `.env.example` советовал URL-кодировать `POSTGRES_PASSWORD` — ломает аутентификацию (сырое значение уходит в Postgres, а DSN его декодирует); не хватало `PADMARK_METRICS_ADDR`, `PUBLIC_SCHEME`, `DISABLE_API`, `DIRECT_REDIRECT`, `CUSTOM_SLUGS` | Совет заменён на «только URL-safe символы» (`openssl rand -hex 32`); недостающие переменные добавлены с дефолтами из `flags.go` |
| N-11 | Low | R-27a | При падении основного listener-а `runServer` возвращался без `CloseContext` — redirect/metrics listener-ы не закрывались | `waitAndShutdown` всегда вызывает `CloseContext` (deadline на `WithoutCancel`), ошибки объединяются `errors.Join` с сохранением `errors.Is`. Тест `TestWaitAndShutdown_ListenerFailureClosesRegisteredResources` |
| N-12 | Low | R-37 | Unit-тесты `usecases/contentkeys` использовали рукописный fake `memoryStorage` — нарушение правила «mocks — mockgen only» | Переписаны на mockgen (`manager_mocks_test.go`, `//go:generate` в `manager.go`) + testify/suite; все сценарии сохранены, добавлены новые |
| N-13 | Low | R-08 | `Origin` не нормализовал регистр host и порты по умолчанию (только ложный отказ); `known_hosts` не проверялся на права при чтении | `Origin` приводит scheme/host к нижнему регистру и убирает `:443`/`:80`; сравнение нормализует и сохранённые строки. Файл, доступный на запись group/other или не regular, считается недоверенным (fail-closed), `trust` лечит права. Тесты `TestIsKnownOrigin_Normalised`, `TestIsKnownOrigin_FailsClosedOnUnsafeFile`, `TestAddKnownOrigin_HealsWritableFile` и др. |
| N-14 | Low | R-40 | Тест на реальный вывод debug-логов MCP отсутствовал; имя флага/env — строковые литералы; у `ValidateLevel` нет godoc | `FlagLogLevel`/`EnvLogLevel`/`DefaultLogLevel` в `mcp/flags.go`; godoc; `TestMCPDebugLogLevel_LogsToStderrOnly`. **Тест выявил реальный баг:** `Manager.Serve` оборачивал `nil` через `fmt.Errorf("…: %w", err)` → каждое штатное завершение MCP-сессии давало exit 1; исправлено |
| N-15 | Info | R-31 | Стек `WrapError` давал «content encryption: content encryption: …» | `WrapError` не повторяет префикс, если он уже есть в цепочке; `failureReport` объединяет ошибки без дублей. Тесты `TestWrapErrorAddsContextOnce`, `TestValidateContentEncryptionAddsContextOnce` |
| N-16 | Info | R-42 | `os.Open` до проверки regular-file → FIFO-путь keyring блокировал старт навсегда | `os.Stat` до открытия; post-open fstat и предупреждения сохранены. Тесты `TestLoadContentKeyring_RejectsFIFOWithoutBlocking`, `…_RejectsDirectory` |
| N-17 | Info | R-18 | `llms.txt`: не сказано, что `POST /notes` игнорирует `ttl` без burn (а PUT применяет как hard expiry); нет 401 на `GET /notes/{id}`; «PUT … same optional fields» подразумевал `slug` | Исправлено; заодно синхронизированы коды ответов links/auth/setup, `code` в ошибках и лимит lockout |
| N-18 | Info | — | Слабые тесты: `TestServerRejectsInvalidSecurityConfigurationBeforeOpeningDB` проверял лишь отсутствие «keyring»; нет защиты от новых `style=`/`on*=` в шаблонах; XSS-тест не покрывал `img src`; нет HTTP-тестов R-11 (422/HTML) | Табличный тест с точными ошибками и проверкой, что БД не создана; `TestTemplatesHaveNoInlineStyleOrHandlers`; `TestRejectsExecutableImageURLs`; `TestSetupJSON_InvalidUsername_422`, `TestSetupJSON_EmptyUsername_400`, `TestSetupHandler_InvalidUsername_ShowsValidationMessage` |
| N-19 | Info | — | Устаревшие комментарии: `editcode.go` («old» `v1$`), `web_note.go` (lockout «per note ID»), обоснование `slugLock` в `links.resolveLink`; README «never exceed 2^32» против кода «well below 2^32» | Исправлены. `slugLock` в `resolveLink` сохранён: межрепличную свежесть даёт fingerprint, локальная блокировка лишь не даёт переналить кэш plaintext-ом только что обновлённой ссылки (цена — сериализация чтений одной ссылки) |

### Неточности исходного текста ревью (исправлены ниже по тексту)

- **P-03** описан наоборот: в golangci-lint 2.13.2 устаревшее имя — `gomodguard`, актуальное —
  `gomodguard_v2`; правка отключила устаревший алиас и тем самым **включила** `gomodguard_v2`.
- **R-33:** список портов был неверен (нет «Login», пропущен APIToken).
- **R-11:** пустой/пробельный username даёт 400 (required-field pre-check), 422 — для непустого невалидного.
- **R-44:** документация **рекомендует**, а не требует `--allowed-hosts`.
- **«Проверено и надёжно»:** GitHub Actions закреплены по major-тегам, не по SHA.
- Устаревшие ссылки на строки (R-02, R-05, R-32), путь миграций (`migrations/`, не `internal/`).

### Осознанно не менялось

- **R-02:** анонимный бакет общий на ресурс — анонимные владельцы edit-code по-прежнему могут быть
  заблокированы анонимным атакующим; защищены только аутентифицированные вызывающие (by design).
- **N-13:** origin, дописанный посторонним, пока `known_hosts` был доступен на запись, остаётся в
  файле после `trust` (права лечатся, содержимое — нет).
- **`links` 422 со slug в тексте ошибки** (`custom link slug %q must be a single path segment`) —
  возвращается только отправителю этого slug и не логируется; не утечка.
- **docker-compose** на PostgreSQL 17, интеграционные тесты — на 18: выбор major-версии оператором,
  не дефект.

---

## Статус сборки (обновлено 2026-10-21, после исправлений N-01…N-19)

- `go build ./...` — ✅
- `go vet -tags=integration ./...` — ✅
- `go test -race -count=1 ./internal/... ./pkg/...` — ✅
- `go test -p 1 -race -tags=integration -count=1 ./internal/infra/storage/postgres/... ./tests/...` — ✅, SQLite и PostgreSQL, без skips
- `bin/golangci-lint run --modules-download-mode=readonly ./... --fix=false` — ✅ 0 issues
- `gofmt -l` (изменённые файлы), `go mod tidy -diff` — ✅ чисто; `.golangci.yml` не менялся
- Оба ранее падавших E2E R-08 проходят с явным trust и изолированным XDG_CONFIG_HOME.
- Удалённые `internal/infra/server/errors.go` и `internal/infra/storage/contentkeys/errors.go` (R-31) — не застейджены (`git rm` при коммите).

---

## High

### R-01. «Мусорные» raw-строки намертво блокируют старт сервера и сам migration-инструмент — исправлено
- **Category:** correctness (доступность / upgrade-path) · **Confidence:** high
- `internal/usecases/contentkeys/manager.go` — `inspect` (~стр. 274–284), `Status` (~стр. 108–111), `PrepareServe` (~стр. 114–124), `initializeRegistry` (~стр. 336–343); `internal/infra/crypto/content_migration.go:12–25` (`ValidateLegacy`)
- **Суть до исправления:** любая строка контента без `$`, не являющаяся валидным legacy-шифртекстом (канонический base64 ≥ 28 байт), даёт `report.Failed++` → `Status` **всегда** возвращает ошибку → `PrepareServe` валит старт сервера, а `encryption migrate` падает ещё в `initializeRegistry` (он первым делом зовёт `Status`).
- **Сценарий:** миграция `20260519000001_expire_legacy_notes.sql` прямо фиксирует существование plaintext-эры («plaintext (pre-encryption) content»): старые БД содержат raw-строки, помеченные expired и никогда не удаляемые (slug в БД не хранится, доступ к ним невозможен, «on next access» никогда не случается). Апгрейд такой БД на эту ветку = сервер не стартует, мигрировать нечем; единственное средство — ручной SQL `DELETE`.
- **Исправление:** `corrupt_raw` отдельно считает структурно некорректные raw-строки. Только эта
  ошибка допускает инициализацию реестра с `phase=pending`; валидные legacy-записи мигрируются,
  а `serve` остаётся заблокированным до завершения миграции. Ошибки ключей, envelope и storage
  по-прежнему не позволяют привязать новую установку.
- **Очистка:** добавлен явный `encryption prune --corrupt --dry-run` и безвозвратный
  `encryption prune --corrupt` для malformed raw без `$` (notes и links, включая expired).
  Он не выбирает валидный legacy или versioned/unknown envelopes. Предварительные ошибки
  проверки ключей/envelope/storage прекращают очистку; compare-and-delete сохраняет конкурентные
  изменения. Команда не инициализирует реестр и не меняет pending на ready — затем нужен `migrate`.
- **Проверки:** unit и CLI с `-race`, SQLite-интеграция с 41 malformed raw-записью,
  сохранением валидных данных/метаданных, CAS-конфликтами и возобновлением после частичного
  сбоя. Общая интеграционная suite пройдена и на PostgreSQL 2026-10-19.
- **Дополнение 2026-10-21 (N-02):** исходный фикс не покрывал plaintext с `$` — такие строки
  классифицировались как envelope и снова блокировали старт без средства лечения. Теперь envelope
  определяется только по тегу `pmc<digits>$`; см. N-02.

## Medium

### R-02. Fail-lockout по ID ресурса — targeted DoS редактирования — исправлено
- **Category:** security · **Confidence:** high
- `internal/adapters/http/middleware_faillock.go:42–78` (на 2026-10-21); подключение `router.go:130–136`
- **Суть до исправления:** счётчик неудачных edit-code ключуется только по `r.PathValue("id")`, проверка порога идёт **до** хендлера, 403 инкрементирует. Владелец с валидной сессией (owner-bypass в usecase его запрос до usecase вообще не доводит) получает 429 наравне с брутфорсером.
- **Сценарий:** ID публичной заметки = её URL. 10 PUT с неверным `edit_code` → все PUT/DELETE (включая владельца) получают 429 на 5 минут; повторяя раз в 5 минут, атакующий держит ресурс permanently uneditable (TTL продлевается каждой неудачей).
- **Исправление:** ключ — пара `(resource ID, подтверждённый user ID)`; `uuid.Nil` выделяет
  отдельный общий анонимный бакет на ресурс. Все сессии и API-токены одного пользователя
  разделяют счётчик; невалидные credentials остаются анонимными. Порог и сброс после успеха
  применяются только к текущей паре, без безусловного bypass для аутентифицированных.
  Отклонённые 429 не продлевают TTL; он отсчитывается от последнего 403.
- **Проверки:** cookie/Bearer владельца продолжают PUT/DELETE после блокировки атакующего;
  проверены ogen notes, native multi-segment notes и links. Сквозной SQLite/auth-тест
  проверяет анонимного и залогиненного атакующего, второй токен того же пользователя,
  bogus Bearer и сохранение чужого lockout после успешной операции владельца.

### R-03. Session ID хранится в БД в открытом виде — «готовый bearer» при утечке — исправлено
- **До исправления:** **Category:** security · **Confidence:** high
- `internal/infra/storage/postgres/session_repository.go:22,89,107`; `internal/infra/storage/sqlite/session_repository.go:22,89,107`
- **Суть:** `session_id` — сырой 32-байтовый ключ, lookup по сырому значению. При этом `api_tokens` уже хранятся как `TokenHash` (SHA-256) — внутренняя несогласованность собственной модели. Пароли — argon2, TOTP-секреты запечатаны, сессии — plaintext.
- **Сценарий:** утечка БД/бэкапа → валидные сессии всех пользователей до 30 дней (`DefaultSessionTTL`), в обход пароля и TOTP.
- **Фикс:** хранить `SHA-256(session_id)` как PK (по образцу `api_tokens`), сырое значение — только в cookie.
- **Исправление:** Сессии сохраняют base64url SHA-256 в session_hash; raw ID остаётся только в cookie/возвращаемом ID. Все lookup/logout/password-change пути хэшируют ID. Миграция 20261014000001 отзывает прежние сессии; SQLite и PostgreSQL тесты пройдены.
- **Дополнение 2026-10-21 (N-05):** Down той же миграции теперь тоже удаляет сессии, чтобы откат не превращал хэши в действующие cookie.

### R-04. Пользовательский `edit_code` хэшируется быстрым SHA-256 — обоснование в комментарии неверно — исправлено
- **Category:** security · **Confidence:** high
- `internal/infra/crypto/editcode.go:22–28` (комментарий) vs `internal/adapters/http/ogen_handler.go:69,263` (`req.EditCode.Or("")`); генерация только при пустом значении: `notes/manager.go:143`, `links/manager.go:473`
- **Суть:** комментарий оправдывал быстрый salted-SHA-256 тем, что «edit codes are high-entropy random strings … cannot be brute-forced» — но API позволяет задать edit_code запросом (например, `"password123"`): при утечке БД перебор пользовательских кодов идёт миллиардами/сек.
- **Исправление:** добавлен `EditCodeHasher.HashUserProvided` — argon2id с `DefaultArgon2Params()` в существующем формате `v1$…`, который `Verify` уже диспатчит в argon2-ветку. Usecases (notes `resolveEditCodeHash`, links `resolveAndHashEditCode`) выбирают вариант по признаку «код сгенерирован сервером / задан вызывающим». Ноль миграции данных: существующие `s1$`-хэши валидны; комментарий в `editcode.go` переписан честно. Тесты: crypto (round-trip, формат `v1$`, разные соли), usecases (пользовательский код → memory-hard вариант, сгенерированный → fast-hash).
- **Остаточный риск (зафиксирован):** уже существующие слабые пользовательские коды, хранящиеся как `s1$`, неотличимы от сгенерированных — они остаются на быстром хэше до пересоздания ресурса. Фикс forward-looking. Стоимость argon2 (64 MiB) платится только при edit-code-авторизации ресурсов с пользовательским кодом; брутфорс ограничен faillock + rate limiter.

### R-05. Privacy-решение redirect-а читается из per-process кэша — в multi-replica окно 30 секунд — исправлено
- **Category:** security · **Confidence:** high (механизм; однорепликовая конфигурация корректна — `resolveLink` держит тот же `slugLock`, refill после evict внутри процесса невозможен)
- `internal/infra/linkcache/cache.go:24` (`DefaultTTL = 30s`); `usecases/links/manager.go` `resolveLink`/`evictCache`; `update.go:70`
- **Суть до исправления:** `Update`/`Delete` эвиктят только локальный кэш процесса. Реплика B до 30 секунд отдаёт расшифрованный TargetURL и применяет privacy-гейт по закэшированному значению; комментарий кэша упоминает только delete/views, не смену privacy.
- **Сценарий:** владелец переводит публичный линк в `privacy=owner` через реплику A; реплика B ≤30s продолжает редиректить анонимов.
- **Исправление:** `Peek`/`Redirect` всегда читают текущую запись из общей БД. Кэш хранит
  только расшифрованный target публичной, неистёкшей, non-burn ссылки и SHA-256 fingerprint
  ciphertext. Метаданные (privacy/owner/TTL/burn/views) не кэшируются; изменившийся ciphertext
  запрещает повторное использование старого URL. Ошибка чтения БД не допускает fallback
  к старой записи. Цена исправления — чтение БД даже на cache hit; расшифровка экономится.
  TTL 30s ограничивает хранение публичного plaintext в памяти, а не актуальность доступа.
- **Проверки:** unit с `-race`, сквозной `TestLinkCacheReplicas` (два HTTP-роутера, общая SQLite,
  раздельные менеджеры/slug-lock/cache) для public→owner/authenticated, смены target, TTL и
  удаления. Обе реплики прогревают кэш до изменений, которые сразу проверяются на B через
  `GET /links/{id}` и `GET /r/{id}`. Полный `go test -race -count=1 ./internal/...` и
  SQLite-интеграции auth/manager/encryption — PASS; PostgreSQL runtime пройден 2026-10-19.

### R-06. CLI `setup` шлёт пароль первого админа и получает TOTP-секрет по HTTP без предупреждения — исправлено
- **До исправления:** **Category:** security · **Confidence:** high
- `internal/adapters/cli/setup.go` (`setupAction`, собственный `http.Client`); сравнить `app.go:70` и `mcp/token.go:58` (там `warnInsecureToken` есть)
- **Сценарий:** `padmark-cli setup --url http://…` — пароль уходит cleartext, ответ с `otp_auth_uri` (эквивалент пароля) возвращается cleartext, ни одного warning'а, при том что для bearer-токена CLI предупреждает.
- **Фикс:** аналог `warnInsecureToken` в `setupAction`.
- **Исправление:** setupAction вызывает warnInsecureSetup перед отправкой; stderr предупреждает о cleartext пароле и TOTP-секрете. Unit и полный AccountsFlow проходят.

### R-07. docker-compose: Postgres с фиксированными кредами опубликован наружу — исправлено
- **До исправления:** **Category:** security · **Confidence:** high
- `docker-compose.yml` — `POSTGRES_PASSWORD: padmark`, `ports: "5432:5432"` (0.0.0.0), DSN `sslmode=disable`
- **Сценарий:** `docker compose up` на хосте с публичным интерфейсом → БД доступна извне с известными кредами; keyring-файл лежит на том же хосте → шифрование контента не спасает.
- **Фикс:** убрать публикацию порта (или `127.0.0.1:5432:5432`); пароль через `${POSTGRES_PASSWORD:?…}`.
- **Исправление:** Compose требует POSTGRES_PASSWORD, использует его в DSN и публикует БД только на 127.0.0.1. README-пример синхронизирован с Compose.

### R-08. Неподписанный `pmk_`-envelope молча направляет весь трафик на произвольный URL — исправлено
- **Category:** security · **Confidence:** high (механизм); эксплуатация — social engineering
- `internal/domain/api_token.go:41–48` (base64url JSON `{url, token}`, без HMAC); `cli/token.go:45–51`, `mcp/token.go:42–48` (`pickServerURL`); `warnInsecureToken` предупреждает только о не-HTTPS
- **Сценарий:** атакующий передаёт «токен для нашего padmark» = `pmk_<base64({url:https://evil.example,…})>`; после помещения в tokenfile весь контент заметок, `Authorization` и edit-code уходят на сервер атакующего; для `https://` warning'а нет вообще.
- **Фактчекинг фикса:** вариант «подписывание envelope» отклонён как не закрывающий атаку — подпись защищает только при наличии у верификатора ключа, которого нет у атакующего, но атакующий здесь выпускает весь envelope целиком (свой токен + свой URL), поэтому HMAC с ключом из самого envelope ему подвластен. Внешнего trust anchor у жертвы нет ⇒ единственный рабочий механизм — TOFU.
- **Исправление (TOFU):** общий trust-store `tokenfile/hosts.go` (`Origin`/`IsKnownOrigin`/`AddKnownOrigin`, `~/.config/padmark/known_hosts`, 0600, XDG-aware, origin = `scheme://host[:port]`). Guard `ensureTrustedServer` в `newPadmarkClient` обоих бинарников: если URL решает envelope (нет явного `--url`/`--base-url`/env) и origin не доверен — fail-closed ошибка до отправки любого запроса, с actionable-инструкцией. Явный URL — сам по себе подтверждение (без изменений). Команда `trust` в `padmark-cli` и subcommand в `padmark-mcp` (stdio-протокол не может спрашивать; вывод строго в stderr) — единственный способ записи, работает headless. Bare-токены и `--url`-флоу — поведение без изменений.
- **Проверки:** unit tokenfile (Origin-матрица, round-trip, дедуп, 0600, fail-closed без HOME), CLI (trust-команда, блокировка, override, полный TOFU-цикл, bare-токены не затронуты, actionable-текст ошибки), MCP (зеркально + выход до старта stdio-сессии); существующий envelope-тест адаптирован (pre-trust). `make lint` — 0 issues, полный `go test -race ./internal/...` — зелёный.
- **Дополнение 2026-10-21 (N-01, N-13):** исходный фикс не покрывал `padmark-cli setup` — команда строила запрос сама в обход `ensureTrustedServer` и могла отправить пароль первого админа на URL из чужого envelope. Теперь `setup` и `newPadmarkClient` проходят единый гейт `resolveTrustedServer`; `Origin` нормализуется, `known_hosts` с небезопасными правами считается недоверенным.
- **Остаточный риск (зафиксирован):** TOFU защищает от перенаправления трафика на чужой сервер только после первой записи доверия; момент первой записи — единственная точка доверия (если атакующий контролирует и самый первый контакт, TOFU не спасает — свойство класса защиты, как в ssh known_hosts).

## Low

### R-09. Invite-токены в БД в открытом виде — исправлено
- **До исправления:** репозитории SQLite/PostgreSQL хранили bearer-токен инвайта в открытом виде; утечка БД позволяла зарегистрироваться в течение его 24-часового срока жизни. · security, high
- **Исправление:** хранится только base64url SHA-256 в `token_hash`; `Consume` и `RedeemInvite` хэшируют входной токен. В `domain.Invite` возвращается только `TokenHash`; сам хэш не принимается как bearer-токен. TTL, одноразовость и транзакционный откат регистрации сохранены.
- **Миграция:** `20261014000002` для обеих БД переименовывает колонку и отзывает старые plaintext-инвайты; администратору нужно выдать новые. Down также отзывает инвайты, чтобы старый бинарник не принимал хэши как токены.
- **Проверки:** тесты хранения/невозможности replay хэша, expiry, rollback при занятом username, конкурентного погашения и SQLite-миграции Up/Down; общая проверка хэша добавлена в SQLite/PostgreSQL auth-suite.
- **Результат:** `go test -race ./internal/...`, SQLite auth-suite и golangci-lint — PASS. PostgreSQL runtime и полный auth E2E-прогон прошли 2026-10-19 после явного trust в CLI-сценариях R-08.

### R-10. `csrfGuard` не fail-closed при nil-секрете — исправлено
- **До исправления:** `verifyCSRFToken` принимал детерминированный HMAC с пустым ключом, если Handler собран без CSRF-секрета; комментарий ошибочно обещал безопасный 400. · security, high
- **Исправление:** `verifyCSRFToken` сразу отвергает `len(secret)==0`; комментарий в `handler.go` описывает фактический 403 на защищённых POST-запросах.
- **Проверки:** nil и пустой секрет отвергают поддельный токен, в том числе с совпадающими cookie/form через guard; хендлер не вызывается. Валидная подпись с настроенным секретом по-прежнему принимается.

### R-11. Username нигде не валидируется — исправлено
- **До исправления:** `usecases/auth/onboarding.go` валидирует только пароль; HTML-ветка `/setup` (`web_auth.go:345`) не проверяет даже пустоту (JSON-ветка — только `TrimSpace==""`); `OTPAuthURI` (`totp.go:97–99`) интерполирует username в otpauth-URI без экранирования. Нюанс фактчекинга: round-trip parse в `OTPAuthURI` отбивает часть «битых» username ошибкой сетапа, но пустой username через HTML-форму создаётся, длина не ограничена. Фикс: `domain.ValidateUsername` + `url.PathEscape`. · security, high
- **Исправление:** domain.ValidateUsername проверяет новые имена до чтения/записи пользователей и погашения инвайта: 1–64 Unicode буквы/цифры/._@-, первый символ — буква или цифра, валидный UTF-8, без пробелов/control/path/query символов. Старые имена продолжают работать на login. OTPAuthURI отдельно экранирует label и query. JSON setup возвращает 422 для непустого невалидного имени (пустое/пробельное — 400 из required-field проверки), HTML показывает понятную ошибку. Тесты покрывают invalid-input/no-storage и URI injection; HTTP-тесты 422/400/HTML добавлены 2026-10-21 (N-18).

### R-12. Неудачная инвалидация старых сессий после смены пароля — fail-soft — исправлено
- **До исправления:** `usecases/auth/session.go:216–222` — `DeleteByUserIDExcept` при ошибке → Warn и успех. Украденная сессия живёт до 30 дней, хотя владелец считает аккаунт «закрытым». Фикс: error-лог + предупреждение клиенту (или retry). · security, high
- **Исправление:** Сбой отзыва логируется на error и возвращает ErrSessionRevocation вместе с новой сессией. HTTP сохраняет replacement cookie, ротирует CSRF и явно сообщает: пароль изменён, старые сессии могут оставаться; полного success-сообщения нет. Unit/HTTP-тесты подтверждают частичный результат. Это предупреждение о реально незавершённом отзыве, не гарантия атомарного password+session commit. Перерисованная форма теперь несёт ротированный CSRF-токен (N-06).

### R-13. Параметры argon2id из хранимых envelope без верхней границы — исправлено
- **До исправления:** `internal/infra/crypto/sealer.go:129–153` (`parseSealed` → u32 без клампинга), `argon2.go:90+` (`verifyArgon2`). Подменённая строка БД (`v1$4294967295$…`) → OOM/зависание логина. Единственный вектор, где недоверенные данные управляют KDF. Фикс: кламп ≤ N× от сконфигурированных параметров. · security (DoS), high
- **Исправление:** До Argon2 проверяются жёсткие пределы: memory<=262144 KiB, time<=10, threads<=16, положительные значения и memory>=8*threads. Stored salt/digest и размер строки ограничены. Некорректный versioned TOTP envelope отвергается без legacy fallback. CLI/env проверяются до narrowing/startup, запись вне bounds запрещена. Параметры не обрезаются: старые допустимые хэши сохраняют ключи; установки с легитимными затратами выше bounds требуют планового reset до обновления. Регрессионные тесты охватывают MaxUint32, нули, слишком большие затраты и длины.

### R-14. `ConvertSlugs` может задвоить `Failed` — исправлено
- **До исправления:** `usecases/contentkeys/migrate.go:159` — `final.Failed += report.Failed` поверх `Status().Failed`, который уже посчитал те же структурно-битые строки. Одна битая запись → `failed: 2`. Фикс: не суммировать дубликат. · correctness, high
- **Исправление:** Ошибки конвертации учитываются по уникальному resource/ID; финальный Status исключает уже посчитанные структурные ошибки. Ошибки расшифровки внутреннего legacy-слоя сохраняются в отчёте. Тесты проверяют оба случая, повторный slug и dry-run.

### R-15. `Repository.Initialize` при конкурентной гонке молча «успешен» без записи — исправлено
- **До исправления:** `infra/storage/contentkeys/repository.go:59–62` — при `ON CONFLICT DO NOTHING` (`count==0`, `err==nil`) `encryptionError(nil)` возвращает `nil` → коммитится пустая транзакция. Фактчекинг-реконсиляция двух ревью: исход сегодня fail-closed (`initialize()` после этого перечитывает registry из БД и валидирует `validateKeys`), но ветка скрывает конфликт и нарушает собственный godoc-контракт «atomically insert state plus checks». Фикс: `if count == 0 { return nil }` отдельной веткой с комментарием (или явной ошибкой). · correctness, high
- **Исправление:** Initialize явно обрабатывает count==0 как победу другого инициализатора, не добавляя свои key checks. Контракт требует перечитать и проверить победивший registry. SQLite/PostgreSQL encryption-suite проверяет конкурентную и повторную инициализацию без смешивания состояния.

### R-16. CSP сервера блокирует inline style-атрибуты в собственных шаблонах — исправлено
- **До исправления:** `router.go:462–472` — `style-src 'self' 'nonce-…'` без `'unsafe-inline'`/`style-src-attr`; по CSP3 nonce не покрывает style-**атрибуты**. Шаблоны их используют: `login.html:72,77`, `setup.html:95,100`, `change_password.html:81–91`, `admin.html:121–185`, `index.html:20`. Верстка молча ломается + шум CSP-violations. Фикс: классы в `style.css` (или `style-src-attr 'unsafe-inline'`). · correctness, high
- **Исправление:** Все style-атрибуты шаблонов заменены классами из static/style.css. Существующая строгая CSP сохранена; nonce-блоки style продолжают работать.

### R-17. Переполнение `time.Duration` при конвертации TTL — исправлено
- **До исправления:** `ogen_handler.go:333` (`ttlToExpiresAt`) — `maximum: 9223372036` есть только у `UpdateLinkRequest.ttl` (openapi.yaml:916); у `CreateLinkRequest.ttl`, `CreateNoteRequest.ttl`, `UpdateNoteRequest.ttl` границы нет → `ttl: 9223372036854775807` даёт `ExpiresAt` в прошлом/произвольном. Фикс: `maximum` во все TTL-поля + регенерация ogen. · correctness, high
- **Исправление:** Во всех четырёх TTL-схемах стоят minimum=0 и maximum=9223372036; сервер, клиент и embedded OpenAPI перегенерированы. Native PUT использует generated UpdateNoteRequest и Validate. Тесты отвергают отрицательный TTL, maximum+1 и MaxInt64; максимальный допустимый TTL не переполняется.

### R-18. `llms.txt` дрейфует от фактического API — исправлено
- **До исправления:** `spec/llms.txt:30` — поле `redirect_to` не существует ни в openapi.yaml, ни в сгенерированных схемах; `llms.txt:40` — «/{id} resolves to 307»: `/{id}` обслуживает только заметки, редирект живёт на `/r/{id}` и отдаёт **301** (`link_handler.go:74`, `StatusMovedPermanently`; сам openapi.yaml:69 пишет 301 корректно). Документ для LLM-клиентов расходится с контрактом. Фикс: убрать `redirect_to`, исправить 307→301 и путь. · correctness (spec drift), high
- **Исправление:** llms.txt описывает metadata + Location: /r/{id}, исключает redirect_to и разделяет путь заметок /{id} с redirect /r/{id}: 301 при direct redirect либо HTML interstitial.

### R-19. Native `PUT /notes/{id...}` не проверяет Content-Type — исправлено
- **До исправления:** `web_note.go:~252–259` — `json.NewDecoder` без `isJSONContentType`; спека обещает 415 для не-JSON на **каждой** операции с телом. На multi-segment slugs клиент получает 400 «invalid body» вместо 415. CSRF здесь нет (PUT = preflight). Фикс: проверка → 415. · correctness, high
- **Исправление:** Native PUT проверяет application/json (с параметрами) до декодирования, иначе отвечает JSON 415. Регрессионный тест покрывает отсутствующий Content-Type, text/plain и form-urlencoded.

### R-20. Нет теста на главный XSS-инвариант рендера: `javascript:`/`data:` href — исправлено
- **До исправления:** `render/markdown_test.go:70–87` — проверены только `<script>` и `onclick`; безопасность единственного `template.HTML` (`web_note.go:96`) держится на bluemonday `UGCPolicy`+`AllowStandardURLs`, чей рефакторинг пройдёт текущие тесты молча. Фикс: кейсы `javascript:`, `data:` в suite. · test-coverage, high
- **Исправление:** Добавлены тесты Markdown/raw HTML для javascript:, data:, смешанного регистра, HTML entity и tab-обфускации. HTTPS, относительные и mailto-ссылки остаются разрешены.

### R-21. Мёртвый ogen-handler `GetNote` без авторизации — исправлено
- **До исправления:** `ogen_handler.go:84–95` — зовёт `manager.View` без `VisibleTo`-гейта; от публичного доступа ко всем private-заметкам через JSON API его спасает **только** отсутствие строки в `registerOgenRoutes` (что подтверждено) + NOTE-комментарий. Одна будущая правка роутера — и приватность сломана без падения тестов. Фикс: gate внутри `GetNote` (как в `GetLink`) или `501`. · security (defense-in-depth), high
- **Исправление:** Ogen GetNote вызывает Peek, проверяет VisibleTo и только затем ViewPreloaded. Прямые тесты метода запрещают приватное чтение до burn/views и подтверждают view для public.

### R-22. Plaintext slug в логах — key material для legacy-шифртекста — исправлено
- **До исправления:** `notes/manager.go:188,372,503–507`, `burn.go:83,103`, `links/manager.go:272–274`, `links/update.go:78` (`"id", slug` на Debug). Slug выводит legacy-ключ (`domain.HashSlug` doc), код старательно не пишет ciphertext в ошибки — но пишет slug в логи. Фикс: логировать `HashSlug(id)`. · security, high
- **Исправление:** Логи usecase notes/links и HTTP-ошибок получают HashSlug(id); с 2026-10-21 и текст ошибки рендера (N-07). Access-log пишет только известные имена роутов (иначе note), а на debug — path_hash вместо пути; query не логируется. Тесты проверяют usecase decryption failures и HTTP info/debug, включая /{slug}.

### R-23. У заметки нельзя снять/изменить plain-TTL (асимметрия с links) — исправлено
- **До исправления:** HTTP-слой Update всегда передаёт `ExpiresAt: nil`; `resolveExpiryTime` наследует `existing.ExpiresAt` при `BurnTTL==0`. Ссылка `PUT /links/{id}` с `ttl: null` снимает TTL, заметка — нет. Если это осознанное API-решение — зафиксировать в openapi.yaml. · correctness, high
- **Исправление:** PUT заметок без burn поддерживает ttl:N от момента обновления, null снимает expiry, omission сохраняет его (отключение burn-timer очищает его). С burn omitted/null/0 эквивалентны (N-08). ClearExpiry — неперсистентная команда usecase. Контракт обновлён; HTTP-тесты покрывают оба типа slug, DB-интеграция — preserve/replace/clear.

### R-24. Секреты через CLI-флаги видны в `ps` — исправлено
- **До исправления:** `--password` (stdin-альтернатива задокументирована), `--token`, `--csrf-secret`. Фикс: упомянуть `ps`-риск в usage / env-only для секрета. · security, high
- **Исправление:** Usage --password, --token (CLI и MCP), --csrf-secret предупреждает о process listings/ps и указывает stdin, env или tokenfile. README содержит те же варианты. С 2026-10-21 также --dsn, --edit-code, --invite (N-09).

### R-25. CLI `get` не обрабатывает `*padmark.GetNoteUnauthorized` — исправлено
- **До исправления:** `cli/get.go:71–86` — ветки NotFound/Gone/ServiceUnavailable/500 есть, 401 уходит в `default` («unexpected response type»); в MCP (`respond.go:43`) и edit (`edit.go:172`) обработан. Фикс: добавить case. · correctness, high
- **Исправление:** CLI get обрабатывает GetNoteUnauthorized понятной ошибкой unauthorized с предложением аутентификации. Добавлен регрессионный тест.

### R-26. `.env.example` документирует несуществующие переменные — исправлено
- **До исправления:** `PADMARK_COOKIE_MAX_AGE` (:47) и `PADMARK_AUTH_TOKENS` (:64) — читателей в коде нет (grep по всему репо). Оператор «включает» write-auth в проде — эффекта ноль. Фикс: удалить. · correctness, high
- **Исправление:** Несуществующие PADMARK_COOKIE_MAX_AGE и PADMARK_AUTH_TOKENS удалены из .env.example; lifetime задаётся поддерживаемым PADMARK_SESSION_TTL.

### R-27. docker-compose ↔ CI: имя образа не совпадает, volume не подключён — исправлено
- **До исправления:** compose: `partydev/padmark:latest`; CI: `vars.DOCKER_IMAGE || 'partyzanex/padmark'` (ci.yml:16). `db_data` объявлен, монтирование закомментировано → данные postgres теряются при recreate. · correctness, high
- **Исправление:** Compose по умолчанию использует partyzanex/padmark, как CI, с DOCKER_IMAGE/DOCKER_TAG override; db_data подключён к /var/lib/postgresql/data (PostgreSQL 17). README-примеры синхронизированы; docker compose config --quiet проходит.

### R-27a. Ошибка graceful shutdown проглатывается — исправлено
- **До исправления:** `infra/server/server.go:~328–336` — при `err != nil` и `context.Cause(serverCtx)==nil` (не уложился в timeout) → `return nil`, exit 0; in-flight запросы оборваны, оркестратор не видит проблемы. Фикс: `return fmt.Errorf("graceful shutdown: %w", err)` при nil cause. · correctness, medium
- **Исправление:** Ошибка CloseContext возвращается как graceful shutdown: %w; ошибка listener/context сохраняет отдельный server cause. Shutdown deadline создаётся после сигнала, а не при старте сервера, на контексте WithoutCancel. С 2026-10-21 shutdown выполняется и при падении listener-а (N-11).

**Валидация R-14–R-27a:** см. актуальный статус сборки выше. Полный SQLite/PostgreSQL
интеграционный прогон 2026-10-19 прошёл без исключённых сценариев.

## Info

### R-28. Случайные 96-бит nonce под долгоживущим KEK — ограничение задокументировано
- `content.go:296–303` — коллизия nonce под одним KEK катастрофична для GCM; при реалистичных объёмах padmark вероятность пренебрежима. Зафиксировать комментарием предел применимости (или per-record subkey в `pmc3`). · security (trade-off), high
- **Решение:** Random 96-bit GCM nonce и суммарный лимит well below 2^32 seals per KEK (все реплики, wrappers, key checks, rewraps) описаны у seal и в README. Автоматического счётчика нет; высокие объёмы требуют другого формата/subkeys, не обещаются текущим pmc2.

### R-29. AAD не содержит версии записи — rollback шифртекста при SQL-доступе на запись — ограничение задокументировано
- `content.go:275–293` — AAD биндит resource+kind+ID, но не версию/эпоху: подстановка более раннего валидного envelope той же записи проходит аутентификацию незаметно. Стандартное ограничение at-rest; задокументировать как принятый риск. · security, high
- **Решение:** AAD аутентифицирует resource/kind/ID, но не freshness. Replay старого валидного envelope на тот же ID остаётся возможен при SQL-write; это явно описано у AAD и в README. Для anti-rollback нужен внешний trusted monotonic state.

### R-30. Тавтологичная проверка legacy-slug в production-пути — исправлено
- **До исправления:** `content.go:320` — единственные runtime-вызовы передают `recordID = HashSlug(note.ID)`, `legacySlug = note.ID` → проверка всегда истинна; реальную работу выполняет slug-keyed GCM. Уточнить комментарий (актуально только для `ConvertSlugs`). · style, high
- **Исправление:** Комментарий ContentEncryptor.Decrypt различает обычный путь (ID уже получен из slug; аутентифицирует legacy GCM) и независимые recordID/slug при конвертации. Проверка сохранена как защитный инвариант.

### R-31. `encryptionError` дублирован в трёх пакетах — исправлено
- **До исправления:** `usecases/contentkeys/errors.go:6`, `infra/storage/contentkeys/errors.go:6`, `infra/server/errors.go:6` — идентичная функция; дивергенция уже почти случилась (R-15). · style, high
- **Исправление:** Остался один usecases/contentkeys.WrapError; server и storage используют его. Две копии удалены. Nil и errors.Is-семантика сохранены; точечные wrapcheck-комментарии объясняют общий contextual wrapper.

### R-32. Тайминговый оракул корректности пароля на логине — принятое ограничение
- `session.go:265–292` (на 2026-10-21) — верный пароль Observatory длиннее (sealer+TOTP). **Задокументировано в коде** и ограничено rate limit 10/мин/IP; предлагается лишь не расширять поверхность (опционально dummy-`sealer.Open`). · security (accepted), high
- **Решение:** Сохраняются dummy password hash, общий factor-error и login rate limit; README и код явно не обещают constant-time end-to-end login. Dummy secret-open на каждый неверный пароль не добавлен: он увеличивает стоимость online DoS и не устраняет системные timing-сигналы.

### R-33. `AuthManager` — широкий агрегат из четырёх интерфейсов — архитектурное замечание, изменений не требуется
- `handler.go:59–70`. Допустимо по правилам проекта (один интерфейс на несколько файлов пакета); отмечено для полноты — при росте сузить до per-consumer. · style, high
- **Решение:** AuthManager остаётся composition-агрегатом существующих портов SessionManager/OnboardingManager/UserAdminManager/APITokenManager. Интерфейсы объявлены в `handler.go` рядом со своим потребителем (структуры `Handler`/`common`), а не в обезличенном `ports.go`. Это допустимая текущая граница; сужение не даёт нового security invariant и не является обязательным исправлением.

### R-34. `requireSameOrigin` покрывает не все session-authenticated мутации — исправлено
- **До исправления:** `router.go:139–143` — комментарий неточен: PUT/DELETE notes/links тоже принимают сессионную cookie (owner-bypass), origin-check не имеют. Сегодня закрыто префлайтом (нет ACAO) + SameSite=Strict; остаточный риск — sibling-subdomain. Фикс: поправить комментарий или обернуть для симметрии. · security (defense-in-depth), high
- **Исправление:** requireSameOrigin теперь оборачивает PUT/DELETE заметок и ссылок, включая native multi-segment routes, до edit-code lockout/usecase. Non-browser запросы без Origin сохраняются. Тесты отвергают cross-origin PUT/DELETE на всех трёх вариантах маршрута без вызовов менеджера. POST-создание (`/notes`, `/links`, `/links/bulk`) намеренно не обёрнуто — JSON-only тело форсирует preflight; комментарий приведён в соответствие, а требования к TLS-прокси задокументированы (N-04).

### R-35. `--disable-api` не отключает JSON/raw-выдачу заметок через веб-маршрут — исправлено
- **До исправления:** `middleware_disable_api.go:23–31` — `GET /{id}` (negotiate → JSON по умолчанию, `?raw=1` → markdown) вне набора; поведение задокументировано в коде, но формулировка флага может ввести оператора в заблуждение. Фикс: уточнить help/README либо форсировать HTML при DisableAPI. · security (docs), high
- **Исправление:** Usage и README явно говорят, что disable-api отключает REST-маршруты, но web /{id} сохраняет negotiation, в том числе JSON и raw. Это не глобальный запрет машинного чтения контента.
- **Дополнение 2026-10-21 (N-03):** прежняя формулировка «web routes keep working» была неверна — редактор работает через JSON API, поэтому при `--disable-api` веб-UI становится read-only; usage/README исправлены, редактор показывает ошибку сервера.

### R-36. Сообщение `Migrate` вводит в заблуждение для навсегда-битых строк — исправлено
- **До исправления:** `migrate.go:76–78` — «retry after stopping concurrent writers» бесполезен для corrupt-строк (см. R-01/R-14; счётчики для различия уже есть). · correctness (UX), high
- **Исправление:** После R-01 Status для malformed raw возвращает actionable prune --corrupt --dry-run / prune / migrate; для других повреждений — keyring/integrity error. Совет о concurrent writers остаётся только для структурно валидных raw, появившихся во время миграции.

### R-37. Пакет `contentkeys` не имеет юнит-тестов — исправлено
- **До исправления:** `usecases/contentkeys/`, `infra/storage/contentkeys/` — ни одного `_test.go` при соседних покрытых пакетах (частично компенсируется интеграционным `tests/encryption/encryption_test.go`). Минимум: миграция при Failed-строках, CAS-конфликты, `Register`/`CanRemove`-гейты с mockgen. · test-coverage, high
- **Исправление:** Usecase unit-тесты (с 2026-10-21 — mockgen + testify/suite вместо рукописного fake, N-12) покрывают malformed raw, CAS/prune conflicts, dry-run, сбои, повторные convert-failures, Register (idempotence/key-ID reuse) и CanRemove (active/referenced keys). В infra/storage/contentkeys добавлены SQLite unit-тесты атомарного rollback Initialize и CAS-preservation. Общая encryption-интеграция пройдена на обеих БД.

### R-38. Устаревшие комментарии и дублированные ветки — исправлено
- **До исправления:** `sqlite/link_repository.go:167–169`, `postgres/link_repository.go:163–166` — комментарий про удалённую колонку `private`; `invite_repository` (sqlite:161–169, 208–215; postgres:177–181) — `if isInviteSentinel(err) { return X } return X` с идентичными ветками; двойные пустые строки в `link_repository.go` и др. · style, high
- **Исправление:** Удалены идентичные sentinel-ветки в Consume/RedeemInvite и неиспользуемый isInviteSentinel в обоих backend; fmt %w сохраняет error identity. В текущих link repositories старые private-комментарии отсутствуют.

### R-39. Неиспользуемые/дублирующие индексы — исправлено
- **До исправления:** `20260508000001_idx_notes_expires_at.sql` — по `notes.expires_at` нет ни одного запроса кроме PK-фильтрованных (sweeper'а для notes не существует); `idx_users_username` дублирует `UNIQUE`. · correctness (не баг), high
- **Исправление:** Миграция 20261019000001 для SQLite/PostgreSQL удаляет idx_users_username и idx_notes_expires_at, Down восстанавливает их. UNIQUE username остаётся; проверки expiration в notes идут после PK lookup. Полные repository/auth/manager интеграции проходят.

### R-40. MCP: Debug-логи недостижимы — исправлено
- **До исправления:** `mcp/app.go:56–60` — логгер жёстко `LevelInfo`, поэтому `check.go:33` (`m.log.Debug(...)`) никогда не выводится; уровень не настраивается. · style, high
- **Исправление:** MCP принимает --log-level/PADMARK_LOG_LEVEL, включая debug; неизвестные уровни отвергаются. Логи и advisories идут через stderrWriter, stdout остаётся MCP transport.

### R-41. MCP tool-outputs без «untrusted data»-фрейминга — исправлено
- **До исправления:** `mcp/notes.go:125`, `output.go:44–64` — контент заметок возвращается как есть; публичная заметка с инструкциями = prompt injection в агент с bearer-токеном и vault'ом edit-code'ов. Inherent для продукта; фикс — абзац в description инструментов. · security (design), high
- **Исправление:** Описание каждого MCP-инструмента помечает note text/titles/target URLs как untrusted data и запрещает трактовать результаты как разрешение раскрыть credentials/edit codes, переключить сервер или выполнить постороннее действие. Тест реального tools/list проверяет framing. Это подсказка потребителю, не sandbox и не гарантия отсутствия prompt injection.

### R-42. `LoadContentKeyring` не проверяет права файла (и symlink) — исправлено
- **До исправления:** `content_keyring.go:28` — `os.Open` без проверки режима, при том что `tokenfile` принудительно чинит 0600; вручную созданный keyring с umask 022 = world-readable KEK. Фикс: `perm&0o077` → warning (по образцу `tokenfile.Secure`). · security, high
- **Исправление:** LoadContentKeyring проверяет Stat открытого файла: только regular files; group/other permissions вызывают warning, symlink — отдельный warning. Managed mounts не ломаются и права автоматически не меняются. Тесты проверяют 0600, 0644 и symlink, отсутствие ключей в warning.

### R-43. Опечатка в `--log-level` молча превращается в `info` — исправлено
- **До исправления:** `infra/server/logger.go:10–20` — ветка `default` без диагностики; уровень — критичный конфиг. · style, high
- **Исправление:** Общий logging.ParseLevel/ValidateLevel используется сервером и MCP; CLI/env с опечаткой отклоняется до DB/listener/protocol startup. Прямой вызов newLogger также больше не делает silent fallback — выдаёт warning. Регрессионные flag-тесты проходят.

### R-44. HTTP→HTTPS redirect отражает Host при пустом allowlist — принятое ограничение конфигурации
- `server.go:161–182` — legacy open redirect как осознанный компромисс, задокументирован в usage флага и комментарии; при заданном `--allowed-hosts` — 400. Отмечено для полноты, приемлемо. · security (documented), high
- **Решение:** Backward-compatible empty allowlist сохранён. Usage/README рекомендуют --allowed-hosts и доверенную Host-политику proxy для строгого redirect. При заданном allowlist посторонний Host отвергается (существующие тесты).

## Процесс / Definition of Done

### P-01. `make lint` падает — исправлено
Текущий golangci-lint проходит с 0 issues без auto-fix. Ниже — исходные замечания.

`exhaustruct_v5` ×2 (`negotiate.go:195`, `ogen_link_update.go:23`), `gocyclo` (`links/update.go:26` — Update = 14 > 12), `govet shadow` (`update.go:55`), `ireturn` (`ogen_link_update.go:16` — ogen-интерфейс, вероятно `//nolint`), `funlen` (`update_test.go:110`), `lll` ×11, `varnamelen` ×3 (`tc`), `testifylint` ×1. Конфиг не трогать — править код / точечные `//nolint` с обоснованием.

### P-02. Postgres-тесты в `internal/` требуют Docker без build-tag `integration` — исправлено
Все шесть PostgreSQL repository-наборов получили `//go:build integration`. В CI они явно
добавлены к интеграционному шагу вместе с `./tests/...`, поэтому покрытие сохраняется.
Обычный `go test -race -count=1 ./internal/...` проходит без Docker. `make test` намеренно
запускает интеграционные тесты с тегом и по-прежнему требует Docker.

При проверке также исправлена совместимость с обновлениями зависимостей в текущем рабочем
дереве: Bun 1.3 (`Count`/`Limit` используют `int64`) и Chroma 2.27 (mode-класс для существующего
CSS включён явно). Модульные файлы не изменялись этим исправлением.
PostgreSQL repository-тесты и все suites из ./tests/... пройдены с -race -tags=integration
2026-10-19 на доступном Docker daemon; исключений и пропусков нет.

### P-03. `.golangci.yml` изменён на ветке — обосновано

В этой задаче конфигурация не менялась. Diff относительно main (уточнено 2026-10-21): в списке
disable `gomodguard_v2` заменён на `gomodguard`. В golangci-lint 2.13.2 `gomodguard` — **устаревший**
алиас, а `gomodguard_v2` — актуальное имя, поэтому фактический эффект — `gomodguard_v2` теперь
**включён** (в main был выключен); unversioned `exhaustruct` добавлен в disable.
Versioned `exhaustruct_v5` продолжает проверять рабочие структуры; исключения для опциональных
CLI Flag/Command и test fixtures оставляют намеренные zero-defaults без огромных literals
(regex `Flag|urcli.Command` шире заявленного — совпадает с любой строкой, содержащей «Flag»).
Это совместимость с текущим набором линтеров и явные границы проверки, а не обход новых ошибок.
Остальные замечания исправлены в коде; публичные изменения конфигурации следует включить
в описание PR. Историческое замечание:

`gomodguard_v2` → `gomodguard`, добавлен `exhaustruct` (+исключения для `Flag|urcli.Command` и `_test.go`). Фиксируется как факт аудита; правки конфига — прерогатива владельца (в PR — с обоснованием).

---

## Проверено и надёжно (положительные результаты фактчекинга)

**Аутентификация/CSRF:** cookie-флаги (HttpOnly/SameSite=Strict/Secure по TLS-детекции), ротация и фиксация-невозможность сессий, анти-enumeration (dummy argon2 + единый `ErrInvalidTOTP`), HMAC double-submit CSRF с `hmac.Equal` и ротацией, недавние CSRF-фиксы (JSON `/setup` — тройной барьер Content-Type+Sec-Fetch-Site+Origin; `/api/tokens`/`/api/invites` — `requireSameOrigin`) — закрыты подтверждённо; TOTP ±1 окно + атомарный replay-счётчик; API-токены — SHA-256 в БД, advisory-lock лимит, 0600 tokenfile.

**Криптография контента:** AES-256-GCM с AAD (resource+kind+recordID; wrap дополнительно биндит keyID+SHA-256(payload) — подмена/перестановка шифртекстов невозможна, покрыто тестами); per-record DEK; fail-closed (без plaintext-fallback, `serve` требует `state=ready && raw=0`); миграция построчным CAS — обрыв/повтор безопасны; ротация без даунтайма; keyring 0600 temp+fsync+excl-link, ключи не в логах/CLI; error-oracle свёрнут (`ErrNotFound`).

**HTTP-поверхность:** open redirect закрыт (`safeNextURL` — host/scheme/`\`/`//`); route shadowing невозможно (`ReservedSlugPrefixes` покрывает все first-segment маршруты один-в-один); markdown — goldmark Unsafe **за** bluemonday UGC + whitelist chroma-классов; ровно два `template.HTML` — оба обоснованы; CSP nonce-script на каждом ответе; negotiate не обходит privacy-гейт (гейт до выбора формата); disable-API покрывает все ogen-маршруты, обход нечистыми путями невозможен; спека синхронна с embedded-копией (тест есть).

**Storage/domain:** SQL полностью параметризован (bun placeholders/`bun.Ident`, whitelist колонок); `sql.ErrNoRows`→`ErrNotFound` последовательно; burn = условный `DELETE … RETURNING` — double-read невозможен; slug-TOCTOU закрыт общим `lock.Locker` в процессе; RedeemInvite в одной транзакции; дрейф sqlite/postgres (RETURNING, advisory locks vs `_txlock=immediate`) корректен.

**Инфра:** доверие XFF/X-Forwarded-Proto строго за `--trusted-proxies` с right-to-left парсом; graceful shutdown через partyzanex/shutdown с таймаутами на всех listener'ах (R-27a исправлен); DSN редaктируется в логах; CI: `-race` в unit+integration, actions закреплены по major-тегам (не по SHA); Dockerfile multi-stage → scratch, `USER nobody`, исключения .env/keyring/БД; MCP stdio-гигиена (advisory только в stderr); `rotate`/`rewrap` достижимы только из CLI — HTTP/MCP-маршрутов к contentkeys нет.

---

## Исходный порядок remediation (обязательные пункты выполнены)

1. **Блокер для релиза с апгрейдом существующих инсталляций:** R-01 (+R-36 как дешёвое дополнение).
2. **Быстрые фиксы с высоким соотношением эффект/усилия:** R-02 (ключ lockout), R-03/R-09 (хэш session/invite ID), R-04 (argon2 для пользовательских edit_code), R-07 (compose-порт), P-01/P-02 (DoD).
3. **Гигиена контракта:** R-17, R-18, R-19, R-26, R-27 — один PR по «машинному surface».
4. **Остальное** — по очереди; R-05 и R-28/R-29 начать с документирования границ применимости.
