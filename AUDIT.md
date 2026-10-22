# Аудит безопасности padmark — red team + devil's advocate

Дата: 2026-10-21
Ветка: `feature/owned-notes` (рабочее дерево с незакоммиченными изменениями, 130+ файлов)
Режим: статический анализ кода, без динамической эксплуатации. Каждая находка подтверждена чтением кода; неподтверждённые подозрения вынесены в отдельный раздел.
Тулчейн: локально go1.26.7, Dockerfile собирает на `golang:1.26.3-alpine`, CI на "1.26".

Важное предупреждение: во время аудита файлы `web_auth.go`, `router.go` и `tokenfile/hosts.go` менялись на диске другим актором. Номера строк соответствуют состоянию на момент проверки и могут сдвинуться.

---

## 1. Резюме

Критических дыр (неавторизованное чтение чужих приватных заметок, SQL-инъекции, XSS, обход CSRF, подделка сессий) не найдено. Ядро аутентификации, авторизации и шифрования спроектировано грамотно и защищено тестами.

Однако продукт обещает три вещи, которые код не гарантирует:

1. **«Burn after reading» не означает «прочитано один раз»** — три независимых обхода (раздел 3, H-1).
2. **«Шифрование at rest» легко сводится к нулю локальным пользователем** — SQLite 0644 плюс проверка прав keyring, которая только предупреждает (M-4, M-5).
3. **«HTTPS за балансировщиком» по инструкции из самого флага даёт cookie без `Secure` и без HSTS** (M-1).

Плюс тулчейн с 13 известными уязвимостями stdlib, включая удалённый краш HTTP/2-сервера (H-2).

| Severity | Количество | ID |
|---|---|---|
| CRITICAL | 0 | — |
| HIGH | 2 | H-1, H-2 |
| MEDIUM | 8 | M-1 … M-8 |
| LOW | 14 | L-1 … L-14 |
| INFO | 10 | I-1 … I-10 |

---

## 2. Методика

- Четыре параллельных направления: (A) auth/сессии/CSRF/rate-limit, (B) authz/ownership/IDOR/ссылки, (C) крипто/хранилище/миграции, (D) рендеринг/XSS/CLI/MCP/конфиг/deploy.
- Каждая находка уровня MEDIUM и выше перепроверена вторым чтением исходников.
- `govulncheck ./...` по всему модулю.
- Проверка `.gitignore` / `git ls-files` на утечки секретов.
- Devil's advocate: отдельно оспорены заявления README и GUIDE.md (раздел 6).

---

## 3. HIGH

### H-1. Гарантия burn-after-reading нарушается тремя способами

Интерстициал «подтвердите прочтение» выдаётся только если контент-негоциация выбрала HTML:

- `internal/adapters/http/web_note.go:446` — `if h.revealStore == nil || negotiate(r) != formatHTML { return false }`
- `internal/adapters/http/negotiate.go:65-81` — отсутствующий `Accept` или `*/*` → `formatJSON` (документированный выбор: «curl и большинство HTTP-библиотек должны получать машинный ответ»).
- `internal/usecases/notes/burn.go:41-42` — JSON-путь идёт через `Manager.View` → `burnNote` → `storage.Consume`. Заметка уничтожена.

**Обход 1 — превью-боты и сканеры ссылок.** Пользователь отправляет ссылку в Slack, Telegram или почту. Unfurl-бот, антивирусный шлюз (Safe Links, Proofpoint) или `curl` делает GET с `Accept: */*` → получает JSON с plaintext и сжигает заметку. Адресат видит 404, а содержимое лежит в логах сканера. Именно от этого сценария интерстициал и должен был защищать.

**Обход 2 — сжигание чужих заметок через `<img>` (CSRF на чтение).**
- `internal/infra/render/markdown.go:57` — `html.WithUnsafe()` (raw HTML в markdown разрешён), `:61` — `bluemonday.UGCPolicy()` пропускает `<img src="/…">`.
- `internal/adapters/http/router.go:475` — CSP `img-src 'self' data:` разрешает same-origin картинки.
- Браузер запрашивает картинку с `Accept: image/*,*/*` и cookie (same-site subresource, `SameSite=Strict` не мешает) → `*/*` → JSON → burn.
- Атака: злоумышленник публикует заметку с `<img src="/<slug-жертвы>">`. Каждый, кто её открывает (в том числе залогиненный владелец), своими cookie сжигает или запускает таймер приватной заметки, которую сам атакующий читать не может. Заодно накручивается `views`. Предусловие — знать slug (тривиально при `--custom-slugs`, иначе 60 бит).

**Обход 3 — `/edit/{id}` читает без сжигания и без проверки срока.**
- `internal/adapters/http/web_edit.go:26` — `h.manager.Peek(r.Context(), id)`, `:54` — `Content: note.Content` уходит в шаблон.
- `internal/usecases/notes/manager.go:187-189` — `Peek` «intentionally returns expired notes as-is», без burn, без удаления.
- Любой, кто знает URL burn-заметки, открывает `/edit/<slug>` вместо `/<slug>` и читает сколько угодно раз. Просроченные заметки так же читаемы до первого обращения к view-маршруту (ленивое удаление, свипера нет). Поведение закреплено тестом `TestEditPage_OK` (`handler_test.go:243-268`).

**Исправление:**
1. Применять burn только при явном намерении: для HTML — после интерстициала; для API — только если `Accept` явно называет `application/json`/`text/markdown` (не `*/*`) или присутствует `Sec-Fetch-Mode: navigate`/`Sec-Fetch-Dest: document`; иначе отдавать интерстициал или 409.
2. Убрать `img` из политики или разрешить только внешние `src` через прокси; дополнительно требовать `Sec-Fetch-Dest != image` для любых побочных эффектов чтения.
3. В `EditPage`: просроченная → удалить + 410; immediate-burn → не предзаполнять content (или требовать reveal-токен). Переписать `TestEditPage_OK`.
4. Добавить фоновый свипер просроченных заметок (для ссылок он есть).

### H-2. Тулчейн с 13 известными уязвимостями stdlib, Docker-образ ещё старее

`govulncheck ./...` на go1.26.7 (все «fixed in go1.26.9»), код задевает их по call-graph:

| ID | Что |
|---|---|
| GO-2026-6617 | HTTP/2 server crash (HPACK encoder race) — удалённый краш без аутентификации |
| GO-2026-6603 | HTTP/2 server memory exhaustion через trailer |
| GO-2026-6611 / 6612 | HTTP/2 CPU / flow-control |
| GO-2026-6609 | Неограниченный парсинг `Range` |
| GO-2026-6608 | Обход лимита памяти в `net/textproto` MIME-заголовках |
| GO-2026-6613 / 6605 | HTTP/1 connection desync (CONNECT) |
| GO-2026-6599 / 6600 | `html/template` context-tracking (вызывается из `web_auth.go:461`) |
| GO-2026-6607 | `crypto/tls` ECH |
| GO-2026-6809 | `crypto/x509` distrust |

- `Dockerfile:1` — `FROM golang:1.26.3-alpine` (продакшен-бинарь собирается на ещё более старом патче).
- `.github/workflows/ci.yml:14` — `GO_VERSION: "1.26"` (плавающий, не фиксирует патч).
- `go.mod:3` — `go 1.26.3`.

**Исправление:** поднять до ≥1.26.9 везде (go.mod `toolchain`, Dockerfile, CI), добавить `govulncheck` в CI как блокирующий шаг.

---

## 4. MEDIUM

### M-1. `--public-scheme https` не включает `Secure` на cookie и HSTS

- `internal/adapters/http/router.go:73-88` — `isHTTPS` смотрит только `r.TLS` и `X-Forwarded-Proto` от доверенного прокси; `forcedScheme` не учитывается.
- `web_auth.go:204, :249, :604`, `middleware_csrf.go:82, :118` — `Secure: isHTTPS(r, …)`; `router.go:497-503` — HSTS по той же функции.
- `internal/infra/server/flags.go:233-242` — справка флага прямо советует `--public-scheme` вместо `--trusted-proxies` для «cloud load balancers, k8s ingress» — то есть ровно в той конфигурации, где `isHTTPS` вернёт false.
- При этом `common.scheme()` и `withRequestOrigin` `forcedScheme` учитывают — код уже считает себя HTTPS для ссылок, но не для cookie.

**Атака:** TLS на балансировщике, padmark с `--public-scheme https` без `--trusted-proxies`. 30-дневная `padmark_session` выдана без `Secure`, HSTS нет. Любая страница с `<img src="http://padmark.example/x">` или on-path атакующий получает cookie в открытом виде → захват аккаунта (админского, если жертва админ).

**Исправление:** `forcedScheme == "https" || isHTTPS(…)` во всех точках; префикс `__Host-` для cookie при `Secure`; поправить `.env.example` (сейчас утверждает, что Secure/HSTS зависят только от trusted-proxies).

### M-2. Первый, кто добежит до `/setup`, становится единственным админом навсегда

- `web_auth.go:278-307`, `web_setup_json.go:62-116` — `/setup` без инвайта открыт, пока `IsEmpty()`; `middleware_auth.go:185-193` — путь публичный; `flags.go:81` — дефолт `:8080` на всех интерфейсах.
- JSON-вариант не требует CSRF (`web_auth.go:480-522`, `setupGuard` намеренно пропускает).
- `migrations/sqlite/20260604000001_auth.sql:39` — partial unique index `is_admin = 1`: админ ровно один, инвайты создают только не-админов (`onboarding.go:108`).

**Атака:** оператор поднял инстанс с `--enable-accounts` на VPS и открыл `/setup` через минуту. Сканер успел раньше → атакующий — единственный админ, владелец восстанавливается только хирургией в БД. Гонка двух админов закрыта индексом — проблема в том, кто победит.

**Исправление:** одноразовый bootstrap-токен в лог/env, обязательный для безынвайтного `/setup`; или принимать первый setup только с loopback / `--bootstrap-allow-cidr`.

### M-3. Защита логина только per-IP: за прокси без `--trusted-proxies` 10 запросов/мин блокируют всех, пер-аккаунтной защиты нет вообще

- `middleware_ratelimit.go:26-28` — `totpRatePerMin = 10`; `:158-171` — ключ только `clientIP`; `:104-123` — без доверенных прокси все клиенты = `RemoteAddr` прокси = один bucket. Код сам это знает (`:174-178`) и лишь один раз пишет warning.
- `usecases/auth/session.go:77-126` — `Login` не считает попытки; `withFailLockout` висит только на PUT/DELETE заметок и ссылок.

**Атака (a):** 10 `POST /totp-login` в минуту (нужна одна валидная пара CSRF cookie + поле, берётся любым GET) → все пользователи за прокси получают 429 на логине бессрочно. **(b):** при правильном прокси — password spraying с ботнета, 10/мин/IP, без локаута аккаунта.

**Исправление:** per-username bucket / exponential backoff в `SessionManager.Login`; отказ в старте (или отключение per-IP лимитера) при наблюдаемых forwarded-заголовках без доверенных прокси.

### M-4. SQLite-файл создаётся 0644, padmark права не ужесточает

- `internal/infra/storage/sqlite/db.go:20-21` — `sql.Open` без `Chmod`/umask; драйвер `modernc.org/sqlite` дефолт `SQLITE_DEFAULT_FILE_PERMISSIONS = 0644`, `-wal`/`-shm` так же.
- `flags.go:83` — `DefaultDSN = "padmark.db"` в CWD.

**Атака:** любой локальный пользователь хоста читает argon2-хэши, sealed TOTP, хэши сессий/токенов/инвайтов, plaintext reveal-токены (L-3), IP/UA сессий и все зашифрованные envelopes. Вместе с M-5 — полное раскрытие.

**Исправление:** создавать файл `O_CREATE|0600` до передачи драйверу или `os.Chmod` после первого ping (включая sidecar-файлы); документировать umask 077.

### M-5. Keyring с широкими правами — только warning, сервер стартует

- `internal/infra/crypto/content_keyring.go:85-88` — `Perm()&0o077 != 0` → `slog.Warn`. Тест `keyring_permissions_test.go:27-30` закрепляет это как желаемое.
- Warning идёт в глобальный `slog` (text, stderr), а не в инжектированный JSON-логгер (`server.go:517-519` не делает `slog.SetDefault`) — в продакшен-пайплайне логов легко потерять.

**Исправление:** fail-closed, исключение только для `/run/secrets/` или явного `--allow-shared-keyring`; провести warning через серверный логгер.

### M-6. API-токен может выпускать новые API-токены, они вечные и переживают смену пароля

- `middleware_auth.go:73-80` — Bearer аутентифицирует любой непубличный маршрут, включая `POST /api/tokens` и HTML-админку.
- `ogen_handler.go:464-488` — `CreateAPIToken` требует лишь `userFromCtx != nil`.
- `usecases/auth/api_token.go:73-81` — `ExpiresAt: nil`, TTL нет.
- `session.go:160-229` — `ChangePassword` удаляет сессии, но не трогает `api_tokens`.

**Атака:** утёк `pmk_…` админа (CI-лог, `ps`, общая машина). Атакующий сразу выпускает до 20 новых (`api_token.go:22`). Жертва отзывает утёкший, меняет пароль и TOTP — 19 токенов атакующего живут вечно; в админке они неотличимы от легитимных.

**Исправление:** выпуск токенов и админ-маршруты — только по браузерной сессии (или TOTP step-up); дефолтный TTL; отзыв всех токенов при смене пароля; поле `issued_by` для аудита.

### M-7. Redoc грузится с `cdn.redoc.ly/redoc/latest` без SRI, CSP расширен под него

- `templates/apidocs.html:11` — `<script src="https://cdn.redoc.ly/redoc/latest/bundles/redoc.standalone.js" nonce=…>`; `web_apidocs.go:31` — `buildCSP(nonce, redocScriptSrc)`.
- `/api` всегда публичен и живёт на origin приложения.

**Атака:** компрометация CDN или тега `latest` → скрипт выполняется same-origin. Залогиненный админ открывает `/api` → `fetch('/api/tokens', {method:'POST'})` проходит `requireSameOrigin` с cookie → вечный API-токен атакующего (см. M-6); CSRF-токен читается из DOM → `/admin/invite`, revoke.

**Исправление:** вендорить `redoc.standalone.js` в `static/` под `script-src 'self'`, либо пиновать версию + `integrity=… crossorigin=anonymous`; убрать расширение CSP.

### M-8. Пользовательский `edit_code` без минимальной длины, локаут процессный и обходимый

- `spec/openapi.yaml:730-735` — `edit_code: type: string`, нет `minLength`; `notes/manager.go:453` хэширует что дали.
- `middleware_faillock.go:15-19, :23, :35-36` — 10 попыток / 5 мин на `(id, callerID)`, in-process ARC-кэш: сбрасывается перезапуском, не разделяется между репликами, каждый зарегистрированный аккаунт — свой bucket.

**Атака:** заметка с `edit_code: "1"` через CLI/MCP → ~2880 попыток/сутки только с анонимного bucket'а, плюс по bucket'у на каждый инвайт-аккаунт → перебор 1–3-символьных кодов → delete/overwrite/смена privacy.

**Исправление:** `minLength: 8` в обеих create-схемах + доменная валидация; анонимный bucket дополнительно по IP; при нескольких репликах — локаут в БД.

---

## 5. LOW

### L-1. CSRF-токен не привязан к сессии, HTML-формы без Origin/Sec-Fetch-Site
`middleware_csrf.go:26-39` — токен = `nonce || HMAC(nonce)`, без материала сессии; `:163-189` — проверка cookie == поле + HMAC. Любой когда-либо выданный токен валиден для любой жертвы. Сценарий: подконтрольный sibling-поддомен ставит `padmark_csrf` с `Domain=example.com` и автосабмитит форму в `/admin/invite` (SameSite=Strict не защищает от same-site). Фикс: HMAC поверх `nonce || sessionHash`, префикс `__Host-`, `requireSameOrigin` на формах.

### L-2. Открытый 301-редиректор по умолчанию
`flags.go:81-96` — аккаунты выключены, анонимное создание ссылок и `direct-redirect` по дефолту → любой инстанс — бесплатный URL-shortener для фишинга с репутацией домена оператора. Цели ограничены `http/https` + host (`links/manager.go:175-192`), но это не анти-abuse. Фикс: интерстициал по дефолту, прямой редирект только для аутентифицированных.

### L-3. Reveal-токены — единственный bearer в БД в открытом виде
`sqlite/reveal_repository.go:53-58`, `migrations/…/20260520000001_reveal_tokens.sql:3` — `token TEXT PRIMARY KEY` plaintext; сессии/инвайты/API-токены уже SHA-256. Читатель БД сжигает чужую burn-заметку до легитимного чтения. Фикс: хранить хэш.

### L-4. Argon2-параметры доверяются из строк БД до жёстких капов (256 MiB × 10 × 16)
`argon2.go:28-33, :123`, `sealer.go:84`. Писатель в БД делает один логин ~50× дороже бюджета. Фикс: принимать стоимость ≤ max(настроенная, default), выше — алерт.

### L-5. Пользовательский edit-code хэшируется argon2 64 MiB на анонимном create, независимо от `--argon2-*`
`editcode.go:61-62` — `DefaultArgon2Params()`; справка `flags.go:257-258` утверждает «edit codes используют быстрый хэш» (верно только для генерируемых). Распределённый `POST /notes` с `edit_code` держит сотни буферов по 64 MiB. Фикс: прокинуть настроенные параметры, поправить текст.

### L-6. Анонимный bucket локаута — `(id, uuid.Nil)` для всех анонимов
`middleware_faillock.go:26-32, :44` — 10 неверных кодов от кого угодно → 429 для легитимного анонимного редактора на 5 мин, повторяемо. Фикс: добавить IP в ключ.

### L-7. Нет кнопки logout, сессия 30 дней абсолютно, старая не отзывается при повторном логине
Ни один шаблон/статика не ссылается на `/logout`; `POST /logout` за CSRF-guard — недостижим руками. `session.go:77-126` не удаляет сессию из входящей cookie; idle-timeout нет. Фикс: форма logout в `header.html`, удалять предыдущую сессию, idle-timeout.

### L-8. `username` уникален case-sensitive
`migrations/*/20260604000001_auth.sql` — `UNIQUE` без `COLLATE NOCASE`/`lower()`. `admin` и `Admin` — разные аккаунты, путаница в админке. Фикс: unique по `lower(username)`.

### L-9. CSP без `base-uri`, `form-action`, `frame-ancestors`, `object-src`
`router.go:467-477`. Сейчас держится на bluemonday и `X-Frame-Options: DENY`; одна регрессия санитайзера — и защиты нет. Фикс: добавить директивы.

### L-10. DOM clobbering через `id` (UGC разрешает глобально) + `WithAutoHeadingID`
`templates/note.html:113, :149, :170` — `getElementById('raw-content')` после тела заметки; заголовок `# raw-content` перехватывает. Только UI-спуфинг. Фикс: префикс `user-content-` или стрипать `id`.

### L-11. Markdown рендерится на каждый HTML-просмотр, единственный кап — 4 MB body
`notes/manager.go:476`, нет доменного лимита content (только title 500). 4 MB патологического MD × chroma × bluemonday × 10 rps/IP. Фикс: лимит 512 KB–1 MB, кэш по `(id, updated_at)`.

### L-12. Edit-code принимается query-параметром на DELETE
`web_note.go:292-295` — попадает в логи прокси/CDN. Фикс: только header/body.

### L-13. `--allowed-hosts` защищает только HTTP→HTTPS редирект-листенер
`server/server.go:161-182`; основной листенер строит абсолютные URL из `r.Host` (инвайты `web_admin.go:116`, envelope-токен `:189`, success `web_success.go:37`). `no-store` исключает cache poisoning, но envelope с чужим хостом возможен. Фикс: allowlist на основной листенер.

### L-14. Секреты в аргументах процесса: `--dsn` без предупреждения, README советует `"args": ["--token", "pmk_..."]` для MCP
`flags.go:125-130` (DSN с паролем, без `ps`-warning, в отличие от `--csrf-secret` `:289-298`); `README.md:466-471` противоречит собственному предупреждению на строке 558. Cleartext HTTP для токенов — только warning, не блок (`cli/app.go:98-114`, `mcp/token.go:79-92`). Фикс: `"env": {"PADMARK_TOKEN": …}` в README, warning на DSN, `--allow-insecure-http` для не-loopback.

---

## 6. Devil's advocate — оспариваем заявления документации

| Заявление (README/GUIDE) | Реальность |
|---|---|
| «Burn after reading» | Три обхода (H-1). Фича даёт ложное чувство безопасности. Либо чинить, либо переименовать в «best-effort». |
| «Content encrypted at rest» | Криптография отличная, но БД 0644 + keyring warn-only (M-4, M-5) = любой локальный пользователь читает всё. Titles в plaintext — документировано, но заголовок часто и есть секрет («пароль от wifi офиса»). |
| «`privacy: authenticated` требует входа» | Любой приглашённый пользователь читает все такие заметки. При выключенных аккаунтах веб-путь отдаёт их всем (`handler.go:217-219` `isAuthenticated` → true при `authMgr == nil`), а API отвечает 401 — несогласованно. |
| «Проверенный `edit_code` — право редактирования» | Это полный контроль: delete, смена privacy (`note_repository.go:239` `privacy = COALESCE(?, privacy)`), TTL, burn, для ссылок — target URL. Аутентифицированный не-владелец с кодом переводит `owner`-заметку в `public`. Ownership, к счастью, не передаётся. |
| «Защита от brute-force» | Per-IP для TOTP (обваливается в один bucket за прокси), ничего per-account, процессный локаут для edit-code (M-3, M-8). |
| «`--public-scheme` для cloud LB» | Ровно эта рекомендация лишает cookie `Secure` (M-1). |
| «Админ выпускает API-ключи» | Ключ выпускает ключи, вечно, и переживает ротацию пароля (M-6). |
| «Один админ — проще» | Один админ + нет TOTP-recovery + нет reset → потеря аутентификатора = хирургия в БД (I-1); первый добежавший до `/setup` — админ навсегда (M-2). |
| «Рейт-лимит 10 rps/IP» | За прокси без `--trusted-proxies` — 10 rps на всех. Код знает и пишет warning вместо отказа. |
| «2^32 GCM-операций на ключ — ротируйте» | Лимит не считается автоматически (I-5). При случайных 96-бит nonce на KEK это операционное обещание, которое никто не проверяет. |
| «Логи не содержат секретов» | Верно для HTTP-логов. Но сессии хранят IP + User-Agent без retention (I-9) — PII at rest. |
| «Scratch-образ, non-root» | Верно, но собран на go1.26.3 с 13 CVE (H-2); compose публикует `0.0.0.0:8080` plain-HTTP (I-6). |

---

## 7. INFO

- **I-1.** Нет recovery-кодов TOTP и операции reset; `/change-password` требует TOTP (`session.go:177-183`).
- **I-2.** Нет семафора на argon2: каждый логин — до двух дериваций (verify + sealer open, `session.go:269-277`); только per-IP лимитеры ограничивают память.
- **I-3.** Ogen-ошибки эхом отдают обёрнутые сообщения (`ogen_errors.go:40-42` — «update note: not found»); секретов нет, но несогласованно с native-путём.
- **I-4.** 404 vs 401 — oracle существования приватных ресурсов (`web_note.go:388-399`); документировано в OpenAPI, create и так раскрывает занятые slug'и (409).
- **I-5.** KEK-wrap со случайным 96-бит nonce без счётчика (`content.go:298-311`); при ≤10^7 операций P(коллизии) ≈ 2^-51. AES-GCM-SIV или HKDF-подключ на запись сняли бы операционное требование.
- **I-6.** `docker-compose.yml:26-27` — `"8080:8080"` на все интерфейсы (БД — `127.0.0.1:5432`); `POSTGRES_PASSWORD=change-me` из `.env.example` не отвергается (`:?` проверяет только непустоту).
- **I-7.** CI: actions пинованы по мажорному тегу, не по SHA; `govulncheck` в CI нет.
- **I-8.** Автомиграции на `serve`, включая деструктивные (`20260519…` экспайрит все заметки, `20261014…` удаляет все сессии/инвайты); нет `--no-auto-migrate`.
- **I-9.** `sessions.ip` / `user_agent` — PII без retention помимо expiry-свипа.
- **I-10.** Slug-lock in-process (`lock/locker.go:43`, ctx игнорируется) — мультиреплика сохраняет TOCTOU note/link на одном slug; `notes.owner_id` без индекса при `ON DELETE SET NULL`; резервированные префиксы case-sensitive (`Login`, `Admin/x` принимаются как slug, lookalike-поверхность); устаревший комментарий о HKDF-info в `crypto.go:94-98` не соответствует коду; дефолт флага argon2 24 MiB подписан комментарием «OWASP minimum 64 MiB» (`flags.go:93-94`).

---

## 8. Что сделано хорошо (честности ради)

- **Ни одного пути чтения/изменения чужой приватной заметки.** Гейт `handlePrivateAuth` идёт до контент-негоциации и до побочных эффектов; `/{id}` и `/notes/{id}` — один хендлер; reveal, edit, ogen — тот же гейт; list/search-эндпоинтов нет; MCP — чистый прокси через сгенерированный клиент.
- **`uuid.Nil` обработан правильно:** `OwnedBy` требует `userID != uuid.Nil && OwnerID != nil`; аноним никогда не равен владельцу; ownership не записывается в update; удалённый пользователь → `SET NULL` → `owner`-заметка fail-closed.
- **Нет mass assignment:** ogen-декодеры скипают неизвестные поля, `owner_id`/`created_at`/`views` отсутствуют в схемах, slug валидируется regex'ом, все запросы параметризованы через bun, `math/rand` не используется нигде.
- **Креденшелы at rest:** сессии, API-токены, инвайты — 256 бит `crypto/rand`, хранятся SHA-256, ищутся по хэшу; пароли — argon2id с самоописывающимся форматом, constant-time, dummy-hash для неизвестных юзеров, forward-миграция параметров на логине; TOTP-секрет запечатан ключом от пароля; TOTP-replay блокируется атомарным `last_totp_counter`.
- **Шифрование контента:** DEK на запись, KEK-wrap с AAD = purpose‖kind‖resource‖record-id‖SHA-256(payload) — подмена строк/ресурсов/ключей ловится и покрыта тестами; keyring только из файла, атомарная запись, подменённый ключ блокирует старт до открытия листенеров; plaintext-режима не существует.
- **Сессии и CSRF:** `HttpOnly`, `SameSite=Strict`, серверная генерация ID (фиксация невозможна), ротация CSRF на логине, HMAC + `hmac.Equal`, `requireSameOrigin` на cookie-мутациях, JSON-setup с Content-Type + Sec-Fetch-Site + Origin; `safeNextURL` отбивает scheme/host/`//`/backslash.
- **Прокси-модель:** XFF обходится справа налево с пропуском доверенных хопов, аборт на мусоре; `X-Forwarded-Proto` только от доверенных; широкие CIDR — warning на старте.
- **Рендеринг:** goldmark → bluemonday UGC (+ строгий allowlist chroma-классов) → `template.HTML`; `javascript:`/`data:` отбиты тестами; raw-ответы `text/markdown` + `nosniff`; nonce-CSP без `unsafe-inline`; `no-store`, XFO DENY, Referrer-Policy (slug'и не утекают в Referer), HSTS при HTTPS.
- **Лимиты:** read-header 5 s, idle 60 s, read 30 s, write 60 s, headers 64 KB, body 4 MB, reveal-POST 1 KB; никакой декомпрессии; никаких исходящих HTTP-запросов по пользовательским URL (SSRF отсутствует как класс).
- **Клиенты:** TLS-верификацию отключить нельзя; токен-файл принудительно 0600, `known_hosts` fail-closed на небезопасных правах, TOFU для embedded-URL; MCP держит edit-codes в памяти, прячет из транскрипта, маркирует контент заметок как untrusted, destructive-тулы аннотированы.
- **Логи:** ни slug'ов, ни query-string, ни токенов, пароль DSN редактируется.
- **Гигиена репо:** `.env`, `*.db`, keyring, логи — в `.gitignore` и `.dockerignore`; `git ls-files` чист; CI `permissions: contents: read`, без `pull_request_target`; scratch + `USER nobody` + `-trimpath -s -w`.

---

## 9. Непроверено (подозрения без подтверждения в коде)

- Поведение `ServeMux` на `//admin` и percent-encoded сегментах (`/%6Cogin`) относительно `isNoteIDPath` — выведено из семантики stdlib, не прогнано.
- Порядок cookie (host-only vs domain) в браузерах для L-1.
- Реальная стоимость рендера 4 MB патологического markdown (L-11) — не замерялась.
- CVE-статус goldmark 1.8.6, chroma 2.27.0, bluemonday 1.0.27 — `govulncheck` не сообщил о вызываемых уязвимостях; отдельно по advisory не сверялись.
- Может ли `pgdriver` включить пароль DSN в текст ошибки соединения (редактируется только стартовая строка лога).
- Postgres-репозитории проверялись выборочно, предполагается зеркало SQLite.
- Вытесняем ли 10k-записный ARC-кэш локаута генерацией 403 на собственных заметках.
- `linkcache` держит расшифрованные target-URL в памяти — не рассматривался.

---

## 10. План исправлений по приоритету

### Немедленно (до следующего релиза)
1. **H-2** — Go ≥1.26.9 в go.mod/Dockerfile/CI + `govulncheck` в CI. Полчаса работы.
2. **M-1** — `forcedScheme` в `isHTTPS`. Одна строка + тест.
3. **H-1** — burn только при явном `Accept`/`Sec-Fetch-Mode: navigate`; `/edit` не отдаёт burn/expired; `img` вон из политики или через `Sec-Fetch-Dest`.
4. **M-7** — вендорить Redoc.
5. **M-4 / M-5** — 0600 на SQLite, fail-closed на keyring.

### Следующий спринт
6. **M-6** — выпуск токенов только по сессии, TTL, отзыв при смене пароля.
7. **M-2** — bootstrap-токен для первого `/setup`.
8. **M-3 / M-8 / L-6** — per-username backoff, `minLength` на edit-code, IP в анонимном bucket.
9. **L-1 / L-9** — CSRF привязать к сессии, `__Host-`, дополнить CSP.
10. **L-7** — logout в UI, отзыв старой сессии при логине.

### По ходу
11. L-2, L-3, L-4, L-5, L-8, L-10 … L-14, I-6, I-7, I-8 — по одному PR на пункт.

### Процесс
- Добавить в CI: `govulncheck`, пин actions по SHA, проверку `go.mod toolchain == Dockerfile FROM`.
- Завести `docs/adr/` с явным threat model: кто противник (локальный пользователь хоста? читатель БД? приглашённый юзер?), потому что половина находок выше — это несовпадение между тем, что обещает README, и тем, от кого код реально защищает.

---

Конец аудита.
