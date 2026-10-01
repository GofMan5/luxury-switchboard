# Фулл-ревью Luxury Switchboard (v1.0.27, коммит 88798a7)

Дата ревью: сессия DeepSeek Harness. Объём: 221 Go-файл (~34 тыс. строк, 80 тестовых),
128 TS/TSX (~8,3 тыс. строк), 4 Rust-файла, релизные скрипты, CI, deploy-шаблоны.

Метод: 8 независимых зонных ревью параллельно (relay-core, relay-translation,
privacy-tunnel, guardrails, providers/keypool/settings, platform/support, frontend,
release/infra) + сквозная верификация оркестратором: каждая находка уровня high/medium
перепроверена по коду лично, плюс полная матрица запусков.

---

## 1. Матрица проверок — всё зелёное

| Проверка | Результат |
|---|---|
| `go vet ./...` (owner) | pass |
| `go vet -tags public ./...` | pass |
| `go test ./...` (owner) | pass, все пакеты |
| `go test -tags public ./...` | pass, все пакеты |
| `go test -race ./...` (полное дерево) | pass |
| `gofmt -l backend` | пусто |
| `cargo check` / `cargo fmt --check` / `cargo test` | pass (5 тестов) |
| `pnpm --dir frontend check` (oxlint, tsc, vitest, vite build) | pass — 117 тестов / 25 файлов, 0 ошибок линта |
| Версия 1.0.27 во всех 6 местах | согласована (tauri.conf.json, Cargo.toml, Cargo.lock, package.json, frontend/package.json, AppVersion) |
| SHA256SUMS.txt ↔ артефакт 1.0.27 | сходится; оба издания в artifacts/release{,-public} |
| Зависимости Go | 3 прямые (go-keyring, x/sys, modernc.org/sqlite — pure-Go, без cgo) |
| Направление зависимостей | domain/application — stdlib-only, нарушений нет (автопроверка по всем пакетам) |
| console.log / fmt.Print в проде | нет; stdout чист (только framed-протокол) |
| Все 25 «именных» тестов из AGENTS.md | существуют и проходят |

Рабочее дерево чистое, old-backup не тронут с момента миграции.

---

## 2. Общий вердикт

Кодовая база необычно высокого качества для проекта такого размера. Контракт AGENTS.md —
не декорация: заявленные поведения почти везде подкреплены тестами, которые проверяют
свойство, а не подстроку; гексагональная архитектура настоящая (domain не знает о
транспорте и хранении); публичная граница туннеля действительно fail-closed; секреты
write-only на каждой границе. Релизная система (6 мест версии, физическое вырезание
изданий, mtime-коллектор, friend-пакет) — зрелая.

Найдено: **4 high**, **14 medium**, **~35 low**. Ни одна не «протекает наружу» (приватность
границы подтверждена), но high — реальные дефекты пользовательских сценариев и стабильности:

1. утечка состояния между guardrail re-roll → тихий обрыв стрима;
2. `history.recent` без байтового бюджета → Insights гаснет во время инцидента;
3. конкурентное чтение мапы без лока → фатальный крэш всего сайдкара;
4. Backup-панель без подписки на стор → весь feedback экспорта/импорта мёртв.

Показательно: все четыре — именно тех классов, против которых проект сам писал тесты
(budget-инварианты, lease-инварианты, shell-survival, feedback-бар) — но в соседней ветке
кода или в составе сценария, которого тест не покрывает.

---

## 3. High

### H1. `bufferTerminal` утекает между guardrail re-roll попытками — тихий обрыв стрима
`backend/internal/slices/relay/adapters/http/server.go:337` + `server.go:879`

`requestUpstream` передаёт `&bufferTerminal` в `requestWithRetry`, а guardrail-цикл в
`ServeHTTP` (строки 353–513) зовёт его повторно. Non-streaming fallback внутри первой
попытки ставит `*terminalStream = false` (879), но `body`/`fallbackActive` — локальные
копии, и вторая попытка получает **исходное** `stream:true` тело с уже сброшенным флагом:

- SSE-ветка (855) пропускается; шлюз, ответивший JSON на `stream:true` (случай, который код
  явно поддерживает), возвращается сырым JSON и пишется прямо в закоммиченный
  `text/event-stream` (цикл 519–542) — парсер клиента читает ноль событий: тихий обрыв
  стрима, прямо запрещённый контрактом;
- если вторая попытка приносит SSE, `expectsJSONResponse("/v1/responses") == true`
  (1361–1368, проверено) заводит тело в JSON-ветку, парсинг падает, а у этой ветки нет
  `streamFailures`-фолбэка — запрос жжёт весь потолок в 64 попытки.

Триггер составной (стрим + fallback + Block-отказ + остаток бюджета), но продукт сам
обещает именно этот сценарий: «committed-стрим перепрашивается». Проверено лично по коду.
Фикс: сбрасывать `bufferTerminal` на каждой внешней попытке (или свежий `*bool` на вызов);
тест: stream-запрос + JSON-отвечающий шлюз + Block-отказ первого ответа.

### H2. `history.recent` не имеет байтового бюджета — Insights гаснет во время инцидента
`backend/internal/slices/activity/adapters/stdio/register.go:47-52`

Хендлер возвращает `{"requests": requests}` сырыми строками. Фронт зовёт его с `limit: 100`
(`frontend/src/features/insights/adapters/stdio-insights-port.ts:14`), строка несёт
`errorDetail` до 4096 рун (`activity/application/service.go:290-291`), и 100 строк ×
4,3–12,5 КБ = 0,4–1,25 МБ против бюджета 252 КиБ → `response_too_large`. Ровно этот класс
починен и **измерен** для `activity.list` (комментарий в `register.go:80-94` приводит
351 КБ на 100 строк), но Insights читает из sqlite-истории те же по форме строки.
Экран гаснет ровно тогда, когда провайдер сыплет ошибками и строки заполнены error-телами.
Проверено лично. Фикс: переиспользовать `withinOneFrame` + `available` («Newest N of M»),
добавить одно-фреймовый тест как у соседей.

### H3. Concurrent map read без лока в `CheckPool` — фатальный крэш сайдкара
`backend/internal/slices/keypool/application/checkpool.go:37-42`

`mu.RUnlock()` на строке 39, чтение `manager.providerRates[providerID]` на 40 — без лока
(`opMu` тоже не взят). Писатели: `EnsureProvider`/`RemoveProvider` пишут ту же мапу под
`mu.Lock()` (`manager.go:169-171, 186-192`), и зовутся из `providers.add/update/delete`
на 32 конкурентных stdio-воркерах (`bootstrap/app.go:165`). `keys.check`, гоняющийся с
правкой провайдера, даёт `concurrent map read and map write` — это runtime-throw, который
`recover()` в `handleRequest` **не ловит**: умирает весь сайдкар. Гонка не покрыта тестами
(поэтому `-race` зелёный). Окно узкое (одно чтение), но последствие — смерть процесса.
Проверено лично. Фикс: одна строка — читать под `mu.RLock`.

### H4. Backup-панель читает стор без подписки — feedback экспорта/импорта мёртв
`frontend/src/features/settings/ui/SettingsPage.tsx:130-131`

`const state = backup.snapshot()` напрямую при рендере; `BackupModel.#set` нотифицирует
слушателей, но подписчиков нет — единственный UI-звонок `snapshot()` без
`useSyncExternalStore`. «Writing…»/«Restoring…», notice «Backup written to <path>» и
restore-отчёт не появляются, пока не случится посторонний re-render (а его не будет —
форма Settings независима). Нарушение бара контракта про loading/feedback. Проверено
лично. Фикс: `useSyncExternalStore(backup.subscribe, backup.snapshot)` + тест.

---

## 4. Medium

### Relay
- **M1. Lease не финишируется при congestion-failover** — `server.go:1098-1111`:
  `continue` после `switchRoute` пропускает `finishLease` (1113), тогда как все остальные
  7 switchRoute-сайтов финишируют. `TestEveryCredentialLeaseIsFinished` не содержит
  flapping-кейс, `TestAFlappingProviderFailoversAfterThreeCongestionAnswers` не проверяет
  баланс. Проверено лично; Impact: теряется outcome-бухгалтерия ключа
  (lastOutcome/authStreak) и ломается инвариат, который тест существует чтобы пинить.
  Флагманская фича 1.0.27.
- **M2. Сырой error-body уходит в activity history** — `noteUpstreamDetail`
  (`server.go:2910-2915`) кладёт весь body, filing на 409/700 — 4096 рун с редакцией
  только секретов ≥8 байт. Провайдеры цитируют отклонённый запрос («Invalid value for
  'messages[0].content': '<текст пользователя>'») → фрагменты prompt в UI, против «никогда
  prompt» в контракте. SSE-путь фильтрует до скаляров (`jsonErrorDetail`) — этот путь нет.
- **M3. Tool-реставрация доверяет Content-Type** — `tools_compat.go:940-943` (+Dispatch
  `server.go:676`): решение event-stream/JSON по label, тогда как guardrails-layer
  принципиально bytes-over-label. JSON-ответ с label text/plain пропускает реставрацию
  алиасов — клиент получает провайдерские алиасы для необъявленных tools.
- **M4. `stripEncryptedReasoning` теряет summary** — `tools_compat.go:483-490` выбрасывает
  reasoning-айтем целиком; комментарий (465-466) и README:92 утверждают «summaries
  survive». Проверено лично: summary живёт в том же айтеме.

### Keypool probe (весь `keys.check` построен на трёх расхождениях с релеем)
- **M5. Endpoint захардкожен `BaseURL/models`** — `probe/prober.go:34`; поле `ModelsPath`
  провайдера игнорируется, 404 считается успехом (`checkpool.go:79-80`) — проверка
  «отмывает» мёртвые ключи для провайдеров с нестандартным путём каталога.
- **M6. Auth-форма расходится с релеем** — `prober.go:59-61`: default Bearer для auto;
  релей для auto+anthropic шлёт `x-api-key` (`server.go:1608-1618`, проверено лично) →
  ложные auth-streak и «dead key» нотисы для работающих ключей.
- **M7. Прокси ключа игнорируется** — пробер без proxy-транспорта; для пулов за
  `socks5://…` `keys.check` деградирует в no-op.

### Platform / release
- **M8. CI не проверяет обычные коммиты и PR** — `.github/workflows/build-desktop.yml:3-7`:
  только `workflow_dispatch` и теги `v*`. Version-gate, `-tags public`, race, edition-strip
  работают только в момент релиза. Проверено лично.
- **M9. SIGTERM не разблокирует stdin read-loop** — `platform/stdio/server.go:87-90`:
  ctx проверяется только после прихода строки. Graceful drain работает на EOF/
  `system.shutdown` (packaged-путь шелла), но сигнальный выход висит до hard-kill.
- **M10. Потери history невидимы** — `activity/adapters/stdio/register.go:72-74` игнорирует
  возврат `Record` (false при полной очереди 4096), batchqueue вечный retry без surface —
  sqlite-outage тихо останавливает историю.
- **M11. Backup несёт plaintext-секреты** — `backup/domain/document.go` + `wiring.go:84`
  (`key.Credential.Reveal()`), файл 0600 с предупреждением. Осознанное решение, в UI
  сказано прямо, но в контракте не оформлено как принятый риск.

### Frontend
- **M12. Auto-discover эффект может зациклиться без backoff** —
  `ModelRoutesPage.tsx:41-43`: `!selectedProviderAvailable` остаётся true в edge-состоянии
  (fallback-провайдер недоступен) → каждая смена phase перезапускает `discover()`,
  абортируя предыдущий — самоподдерживающийся цикл.
- **M13. Mojibake в UI-строках** — `ModelRoutesPage.tsx:400,404,425`: литеральные U+FFFD
  в «Loading…»/«Saving…» Chain Wizard. Проверено лично.
- **M14. Параллельная палитра хардкода** — ~20 raw hex вне токенов: 5 почти одинаковых
  синих (#72a3d4/#74a1d0/#79a7d8 рядом с токенами #709bc7/#7ca7d2), 4 danger-оттенка,
  копипаст field-стиля в 6+ модулях. Токен-система есть, но обходится.

---

## 5. Low (сгруппировано)

**Relay:** отмена во время guardrail re-roll wait выходит без `writeStreamFailure` на
коммитнутом стриме (496-501); стриминговый ответ копируется трижды (buffer → tool restore →
review, ~3×32 МиБ транзиентно); нет wall-clock cap на попытку стрима и WriteTimeout на
релейном сервере (триблинг-апстрим/залипший клиент держат запрос до отмены);
provider-специфика в общем коде релея против контракта («workaround живёт в profile»):
`defaultImageUpstream = "gpt-5.6-sol"` (image_compat.go:14), aieva `/chat-completion`
(server.go:1285-1303), GLM-ранги (2445-2447); server.go смешивает 5 ответственностей
(классификаторы ошибок ~550 строк / sseInspector / retry-лестница / два дублирующихся
entry-flow ServeHTTP+Dispatch с дрейфом клонирования заголовков) — швы чистые, резать
легко; `declareBetaFeature` схлопывает multi-line Anthropic-Beta до первой строки
(3249-3259, Get+Set — проверено лично, comma-форма выживает); fast-path scan
framing-чувствителен (tools_compat.go:574); Responses→Chat теряет `text.format` и
`previous_response_id`; images теряет `n`/`response_format`; `freeformToolRejected`
маркеры шире комментария.

**Guardrails:** `.done`-события сплайсят полный payload на delta-аккумулятор (удвоение
сканирования, seam — extract.go:377-386); unnamed items / одинаковые index делят один
аккумулятор; **excerpt находок не проходит маркер-редакцию** — эхо-креденшл в 200 байтах
от матча правил попадает в журнал/событие/UI (проверено лично — на пути findings нет ни
одного redact); CR-only SSE деградирует до raw-скана.

**Publictunnel/tunnel/hub:** `X-Tunnel-Client-IP` доверяется безусловно — инвариант живёт
только в Caddy-шаблоне (и закреплён тестом в bootstrap — хорошо), но выпадение одной строки
шаблона молча включает подделку identity для RPM/банов; `LoadProfiles` тихо дропает
нечитаемые строки хранилища (бан «исчезает» вместо ошибки); literal `4` в
`newMarkerRedactor` (sanitize.go:529) — третья копия ширины, названной «один раз»
(проверено лично); hub-control без ownership-проверки (документированная shared-trust
модель — оставить осознанной); gate на bridge даёт running-oracle по tunnel-ID;
Windows-lock хаба process-local.

**Keypool/providers:** duplicate key ID на load навсегда лочит пул (dormant);
Save-then-Replace расхождение диска и памяти; `providers.list`/`routes.list` без байтового
бюджета (keys.list — с бюджетом и честным `available`).

**Platform:** `analytics/jsonfile/prices.go` пишет temp+rename без fsync, хотя есть
`atomicfile` с Sync (проверено лично); `_ = server.Emit(...)` глотает все ошибки —
oversize-событие исчезает молча; migration publish не crash-atomic между файлами;
builtin-профили и тексты нотисов в composition root; мёртвые якоря
`backup/adapters/live/wiring.go:19-20`.

**Frontend:** identity «своего» туннеля по строке `'Ваш коннект'` (литерал в 4 местах:
1 TS + 3 Go); кнопка «Try again» в WorkspaceBoundary не протестирована (мутационная опора
теста — незакрытый `client.count` в ClientsPage:113); Overview перезагружает insights на
каждый mount без idle-check; дубль `aria-label="Main navigation"` на aside+nav;
`key={index}` в реордерабельном ChainWizard.

**Release/infra:** mtime-коллектор честен, но полупрочен к clock-skew назад; public-strip
проверка — выборка маркеров (не весь owner-набор); Docker-базы tag-pinned без digest;
`.dockerignore` не зеркалит ключевые паттерны `.gitignore` (`*.pem`, `id_ed25519*`,
`model-tunnel*`); **лимит фрейма 256 КиБ дублирован в Rust и Go без drift-теста** (для
allowlist такой тест есть — для лимита нет); `sidecar_write` блокирующе пишет до 256 КиБ
под мьютексом child (wedged-сайдкар паркует lock, мешает stop/exit); `version.mjs`
переписывает первый `"version"` ключ (сегодня корректно, хрупко) + устаревший комментарий
«five surfaces».

**Observability (оркестратор):** Rust-шелл глотает stderr сайдкара (`sidecar.rs:287-289`),
а у GUI-приложения нет родительской консоли — все структурированные логи Go и два fatal-
сообщения main.go в проде не читает никто. Диагностика владельца недоступна вне dev-режима.
Простая правка: пайпить stderr в файл в appdata или в WebView-консоль через отдельный
event. Гигиена: в рабочем дереве лежит untracked `deploy/__pycache__/tunnel_hub.cpython-314.pyc`.

---

## 6. Расхождения контракта с реальностью

| Утверждение (AGENTS/README) | Реальность |
|---|---|
| «набор… сейчас v6» (абзац vendored-data) | rules.json = **v7** (соседний абзац сам говорит v7) — внутренний дрейф документа |
| `looksLikeEventStream` | Символа нет нигде; поведение реальное через `expectsJSONResponse` — дрейф имени |
| «setState в componentDidUpdate линтер запрещает» | `.oxlintrc.json` содержит только rules-of-hooks и only-export-components — правила нет |
| DoD: «Go проходит format» | В CI нет gofmt-проверки (локально — чисто) |
| «keeping the tool exchange and the summaries» (README:92) | Summary выбрасывается вместе с sealed-айтемом (M4) |
| «очереди… неограниченное ожидание ≠ неограниченная память» | Очередь ограничена счётом (10k), но каждый waiter уже держит прочитанное тело — совокупная память = waiters × MaxRequestBytes |
| «virtualization» больших списков | Memoized окна по 180 строк — адекватно измеренному кейсу, слову не соответствует |
| «единая система tokens» | Ядро токенизировано, параллельная палитра ~20 hex (M14) |
| Race в CI | Полное дерево локально; в CI — 7 пакетов, только Linux |
| Oversized frame «убивает… всё приложение» | Убивает сайдкар с `protocol-error`; окно приложения живо (fail-closed, но wording сильнее факта) |
| `platform/userenv` | Пакета нет — env-фильтрация живёт в openssh `childEnvironment` |

Всё остальное из проверенного — подтверждено (см. §7).

---

## 7. Сильные стороны (подтверждено кодом и тестами)

- **Архитектура**: 13 вертикальных слайсов, domain/application stdlib-only (автопроверка),
  узкие типизированные порты между слайсами, composition root только собирает (кроме
  builtin-профилей — low).
- **Протокол stdio**: один лимит 256 КиБ на обе стороны, payload-бюджет с проверенным
  инвариантом конверта, malformed/oversized → контролируемая ошибка, panic-изоляция
  воркеров, дедуп JSON-ключей и лимит вложенности, cancellation по correlation id,
  stdout зарезервирован.
- **Приватность туннеля**: fail-closed на каждой ошибке, нейтральные тела/статусы,
  safeError-allowlist из 4 кодов, отказ неотличим от недоступного провайдера (тест
  проверяет статус+тело+заголовки+errorCode); санитайзер: приватные поля/переименование
  model независимо от длины/id-хэширование/SSE-канонизация с обязательным terminal;
  маркеры vs секреты как в контракте; бан до очереди/роута/тела; бан = только записанное
  решение; Caddy-шаблон перезаписывает клиентский IP и закреплён тестом.
- **Guardrails**: двусторонне доказанный literal-префильтр (правило не теряется и не
  придумывается), dedup по sourceKind с тестами атаки порядком, fold бухгалтерии,
  все заявленные обходные векторы закрыты кодом+тестом, правила не покидают бинарь.
- **Relay**: retry-таксономия с точными счётчиками попыток и ±таблицами классификаторов,
  content-policy как отдельный терминальный класс, канонизация путей на всех девяти
  decision-сайтах при собственном URL апстрима, гигиена заголовков (эхо-ключи режутся),
  отмена на смене провайдера (тест), guardrail re-roll с биллингом всех попыток.
- **Keypool/secrets**: DPAPI current-user + entropy, Linux keyring fail-closed без
  plaintext-фолбэка, Availability-гейт во всех четырёх сторах, ключи write-only везде,
  FIFO-справедливость очереди.
- **Frontend**: физическое вырезание изданий литералами `__OWNER_EDITION__` на месте +
  байт-проверка публичного бандла; WorkspaceBoundary вокруг рабочей области с key={route}
  и реальным мутационным тестом через живой шелл; useSyncExternalStore везде (кроме H4);
  25 поведенческих тест-файлов без снапшотов; версии только из handshake.
- **Релиз**: версия из одного списка в 6 местах с гейтами до компиляции, оба издания за
  один прогон с раздельными SHA256SUMS, fail-closed mtime-коллектор, friend-пакет из
  4 явных файлов с запретом dirty-источника, никаких секретов в проверенных поверхностях.

---

## 8. Приоритеты

1. **H1–H4** — каждый фикс маленький (1–15 строк) + тест; H1 и H2 закрывают классы, которые
   проект уже объявил закрытыми (terminal event, байтовый бюджет).
2. **M8** — CI на push/PR (check-only job) — дёшево, меняет модель качества радикально.
3. **M1** — `finishLease` перед `switchRoute` + flapping-кейс в lease-таблицу.
4. **M5–M7** — пробер: ModelsPath, auth-shape как в релее, proxy-транспорт (или честно
   вынести ограничение в UI).
5. **M2** — скалярная экстракция error-detail на пути 4xx/5xx.
6. **M10, M4, M13** и low-находка про редакцию excerpt'ов — наблюдаемость и соответствие заявленному.
7. Остальное — по вкусу; drift-тест лимита фрейма и gofmt в CI стоят копейки.
