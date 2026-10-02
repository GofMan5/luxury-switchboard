# Ревью-цикл 3: закрытие остатков + повторное ревью (Luxury Switchboard 1.0.31)

Третий цикл: закрывает остатки из §4 [FULL-REVIEW-ROUND2.md](FULL-REVIEW-ROUND2.md), поднимает патч
1.0.28 → 1.0.31, прогоняет полную матрицу и повторяет независимое ревью четырьмя зонами.
Найдены и починены **три настоящих живых бага** (подсчёт токенов ×2, буферизация SSE/TTFT, §3).

---

## 1. Relay (server.go / tools_compat.go / format_compat.go / image_compat.go)

## 1. Relay (server.go / tools_compat.go / format_compat.go / image_compat.go)

| Что | Где | Тест |
|---|---|---|
| Отмена во время guardrail re-roll wait завершает закоммиченный стрим terminal-событием (раньше — тишина после headers) | server.go | `TestACancelledReRollStillEndsACommittedStream` (мутация убита) |
| Fast-path триггер-скан читает **дефреймленные** байты (`streamAnnouncesRepairableEvents`) — литерал, разрезанный фреймингом, больше не пропускает починку | tools_compat.go | `TestTheTriggerScanReadsTheDeframedBytes` |
| `freeformToolRejected` требует tool-контекст рядом с маркером (`markerInToolContext`, окно 64) — «custom instructions are limited» больше не тратит даунгрейд | server.go | расширен `TestAnUnrelatedRefusalKeepsFreeformToolsIntact` (4 новых кейса) |
| `text.format` → `response_format` (json_object и json_schema с name/strict/schema); `previous_response_id` задокументирован как принципиально непереводимый (нет серверного состояния в chat) | format_compat.go, `chatResponseFormat` | `TestTheChatTranslationCarriesStructuredOutput` |
| images: `n` доезжает до tool'а, **каждая** картинка возвращается своей записью в порядке провайдера (`imageCollection` с порядком обхода; раньше бралась одна «самая длинная») | image_compat.go | `TestSeveralRequestedImagesTravelAndComeBack` (стрим + буфер, порядок) |
| **Chat-провайдер, отвечающий JSON на stream-запрос** (`chatCompletionBody`): полный ответ не умирает в 55-секундном цикле ретраев — раньше это был 502 при ответе в руках | server.go, bufferTerminalSSE EOF-ветка | `TestChatCompatBufferedUsageReachesTheActivityRecord` |
| Re-roll восстанавливает entry-путь и entry-флаг диалекта — после mid-request chat-discovery лестница повторно выводит трансляцию | server.go | `TestARerolledChatDiscoveryWalksTheSameLadder` |
| Unrepairable тела стримятся нетронутыми; oversize/broken-read **сплайсится обратно** (как guardrail-слой), не маскируется усечкой; framing решают байты | tools_compat.go | 4 комбинации в `TestToolNamesAreRestoredWhateverTheAnswerIsLabelled` |
| Скраб эхо запроса выполняется **до** rune-границы (`jsonErrorDetailUnbounded`) — длинная цитата не остаётся неусечённым префиксом; тот же порядок в Dispatch-фоллбеке | server.go | `TestARequestQuotedBackInAnErrorDoesNotBecomeHistory` |

### 1.1 Находки повторного ревью (все закрыты в этом же цикле)

| Находка | Закрытие |
|---|---|
| **[high]** `chatCompletionBody` срабатывал вне chat-диалекта: JSON под меткой `text/event-stream` — guardrails читали его как SSE, находили **ноль** pieces и пропускали payload в block-режиме (вновь открытый класс «mislabel») | Тройное закрытие: (1) гейт на `chatDialect` в `bufferTerminalSSE`; (2) честная метка — terminal `chat.completed` → `application/json`; (3) `reviewResponse` выбирает диалект **байтами** (`bodyLikesEventStream`), не меткой. Тест: `TestANativeChatStreamGivenJSONIsNotDeliveredUninspected` (benign+malicious, мутация гейта убита — «JSON answer spliced into a committed event stream») |
| **[high]** Миграционный `anyExists`-гейт делал per-file восстановление недостижимым: краш между rename'ами → «файл существует → больше не мигрировать никогда» | Гейт удалён; `importEachMissing` сам пропускает приземлённые файлы и молчит, когда нечего делать |
| [low] `.done`-суффикс осиротил lookup имени tool'а (`tool_call:unknown`) | `toolSource(strings.TrimSuffix(key, "_done"))` + тест `TestADoneEventsArgumentsKeepTheAnnouncedToolName` |
| [low] `streamAnnouncesRepairableEvents` не нормализовал `\r\n` — fast-path пропускал починку при CRLF-фрейминге | Нормализация `\r\n`/`\r` перед дефреймингом |
| [low] `markerInToolContext` считал «type» tool-контекстом — стандартный error-envelope реоткрывал даунгрейд | «type» убран из списка соседей (все позитивные кейсы содержат «tool») |
| [low] Dispatch-фоллбек усекал до скраба (порядок, обратный инварианту) | `jsonErrorDetailUnbounded` → scrub → `truncateErrorDetail` |
| [low] `imageCollection` O(n²) на вложенных массивах | Одна аллокация для нового стека |
| [info] Документация `TrimToPayloadBudget` называла только newest-first | Переформулировано: «least valuable end» |
| [info] Mojibake в live_usage_test.go | Исправлено |

## 2. Guardrails (extract.go)

| Что | Тест |
|---|---|
| `.done`-события не сплайсят полный payload на delta-аккумулятор (свой ключ `_done` — как item-путь делал всегда) | `TestADoneEventDoesNotSpliceOntoItsDeltas` (мутация убита) |
| CR-only line endings нормализуются (`\r` → `\n`) — CR-only SSE больше не деградирует в raw-скан | `TestCRLineEndingsAreReadAsEvents` |
| Безымянные items получают собственный аккумулятор (счётчик `anon_`) — два разных безымянных вызова не конкатенируются | `TestUnnamedItemsDoNotShareAnAccumulator` |
| Переиспользованный индекс Anthropic-блока = новое поколение (`anthropicStarts`) — второй блок не сплайсится на первый; deltas читают текущее поколение | `TestAReusedAnthropicIndexDoesNotSpliceBlocks` |

### 2.1 Регрессия, найденная повторным ревью (закрыта)

Счётчик `anon_` на **event**-пути сломал аккумуляцию безымянных дельт — payload, разрезанный
на события без `item_id`, больше не склеивался (тот самый split-payload обход, ради которого
аккумулятор существует). Исправлено: дельты (фрагменты) снова делят общий ключ `output_`,
anon-ключи получают только `.done`-события (полные значения). Тесты:
`TestUnnamedDeltasStillJoinTheSharedAccumulator` (мутация убита), `TestUnnamedDoneEventsDoNotShareAnAccumulator`.

## 3. Живой e2e: два настоящих бага подсчёта токенов

Пользователь сообщил: на alpha-relay (chat-диалект) нули на всех экранах. Разбор:
- **Живое e2е** (`live_usage_test.go`): настоящий app через stdio — providers.add → keys.add → routes.upsert → relay.start → **реальный HTTP POST** → activity.list.
- **Баг №1**: chat-провайдер, игнорирующий `stream:true` и отвечающий JSON → 55 секунд ретраев → 502. **Починено** `chatCompletionBody` (§1, строка 6).
- **Баг №2 (главный)**: перевод `/v1/responses` → chat **не просил streamed usage**. OpenAI-совместимый провайдер кладёт usage в стрим **только** при `stream_options: {"include_usage": true}` — alpha-relay честно молчал, релею нечего было считать. Мой первый e2e-фейк слал usage всегда и **маскировал** это. Починено: трансляция просит usage ([format_compat.go](backend/internal/slices/relay/adapters/http/format_compat.go)), fallback убирает stream_options при stream:false. Живое e2е переведено на «честного» провайдера (usage только по запросу) — **мутация (убрать include_usage) роняет его ровно симптомом пользователя: нули**.
- **Баг №3**: клиент пользователя ходил нативным `POST /chat/completions` **без `/v1`** — а `requiresStreamTerminal` и ещё ~10 мест признавали только префиксные написания → стрим не считался terminal → не буферизовался → не инспектировался → usage не читался. Починено семейством диалект-хелперов (`chatDialectPath` и пр., оба написания) + `ensureStreamUsage` на нативный chat-путь (relay сам просит usage для своего счётчика).

## 3.2 Буферизация SSE (TTFT) — «выплевывает кусками»

Друг пользователя описал симптом: ответы приходят кусками, буферизуясь. Замер на живом app
(`TestLiveRelayStreamDeliveryTiming`): **gap между событиями = 0s** — клиент ждал всю генерацию
(у них — до 45s) и получал всё разом. Причина: дизайн «guardrails inspect before bytes» буферизовал
каждый ответ целиком.

**Фикс — трёхгейтная live-отдача** ([live_stream.go](backend/internal/slices/relay/adapters/http/live_stream.go)):
1. **Режим**: monitor — вердикты не блокируют отдачу, байты можно пускать как идут; block — буферизация (вердикт решает, увидит ли клиент байт).
2. **Диалект**: только нативный (без трансляции/images — им нужна конвертация целого ответа).
3. **Пробационное окно** (250ms, `LiveStreamProbation`): стрим, переживший окно, идёт live; оборвавшийся внутри — клиент не видел ни байта → невидимый ремонт лестницей как раньше.

Прочее закрытое по пути: cancel контекста убивал live-тело (транспорт закрывает body по cancel —
withHeartbeat теперь не отменяет контекст для live); дедлок горутины-читателя (снапшот `source` —
пере-присвоение `response.Body` замыкало цикл «горутина читает канал, который кормит сама»);
гонка heartbeat×copy-loop (восстановлен edge `<-exited`); префикс пробации доставляется
MultiReader'ом; idle-watchdog на live-чтении.
Результат: **TTFT ≈ окно + первый токен** (замер: 252ms при пейсинге 400ms), мутация
(выключить probation → liveAfter=0) роняет тест с TTFT=403ms — вся генерация.

### 3.3 Ревью live-стриминга — три прохода (всё закрыто)

- **Проход 1** (2 ревьюера): [high] channelReader терял байты за пределами буфера вызываемого и паркинговал навсегда на потерянной ошибке; [high] image-мост шёл live по переписанному пути; Dispatch шёл live с нулевым учётом; live пропускал repairs.merge и tool-repair. **Все закрыты** (`4302fc6`): хвост буферизуется в pending, закрытый канал = явная ошибка; liveAllowed (image/compat-флаги + Dispatch=false); merge и лиз до конца стрима.
- **Проход 2** (финальная верификация): все механики подтверждены; остался кластер «префикс обрабатывается дважды» (review-тело = prefix+prefix+rest, парсер инспектора сбрасывался на срезе окна) и гейт «отсутствие триггеров» вместо позитивного доказательства. **Закрыто** (`e09ff03`): collected = ровно доставленные байты, inspector пропускает префикс; гейт на позитивное доказательство — chat-семья live по окну, Responses только при открытом lifecycle (annoncement не pre-completed).
- Проход 3 подтверждал исправления проходов 1-2 и не нашёл блокеров на байтовом пути, лизе или Dispatch.

## 3.1 Пробные 400 (лишнее время на каждом запросе)

Codex на GLM платил **один пробный 400 на каждый запрос**: Codex просит `reasoning_effort:"medium"`,
GLM принимает low/high/max и говорит об этом в тексте ошибки. Ремонт (map на ближайший уровень)
срабатывал — 200 — но round trip уже потрачен, и в Live Activity строка мигает 400→200.
Починено **запоминанием** (по образцу chat-discovery кэша): [repair_memo.go](backend/internal/slices/relay/adapters/http/repair_memo.go) —
relay учитывает по провайдеру выученную форму запроса (роль developer→system, rename max_tokens,
отказанные параметры, список принимаемых effort-уровней) и применяет её **до** отправки. Первый
запрос платит за пробу (400 — единственный честный источник), все следующие идут сразу.
Обучается **только успешный** запрос: лежащий провайдер ничего не «чинит» из следующего.
Тесты: `TestTheSecondRequestSendsTheLearnedShape` (мутация убита: без превентивного применения
второй запрос снова платит пробу), `TestAFailingRequestTeachesNothing`, `TestTheMemoRewritesExactlyWhatTheRepairsDo`.

## 4. Platform / slices / release / Rust / frontend

| Что | Где | Тест |
|---|---|---|
| Emit-ошибки пишутся в Diagnostics (stderr) — событие, не влезающее во фрейм, больше не исчезает молча | platform/stdio + bootstrap | `TestAnUndeliverableEventIsReportedToDiagnostics` |
| `TrimToPayloadBudget` — общий бюджет-хелпер, **четыре** ручных копии (keypool/activity/guardrails/tunnelclients) делегируют ему | platform/stdio/protocol.go | все 4 suite зелёные |
| prices.go → `atomicfile.Replace` (fsync, durability при потере питания) | analytics jsonfile | suite зелёный |
| LoadProfiles: нечитаемые строки хранилища считаются и возвращаются ошибкой (бан «исчез» → видно) | tunnelclients service.go | suite зелёный |
| Duplicate key ID на load: дедупликация (одна копия), пул не лочится навсегда с ложным «secure storage unavailable» | keypool manager.go | обновлён pin-тест |
| Миграция: **per-file re-runnable** (`importEachMissing`) — краш между rename'ами больше не stranded пол-миграции навсегда (гейт `anyExists` удалён по находке ревью) | bootstrap migration.go | `TestTheLegacyImportCompletesAcrossRestarts` |
| removeStaleBundles: одноимённые стейл-артефакты удаляются до сборки (mtime-коллектор видит только файлы этого прогона) | build-release.mjs | node --check + live-прогон |
| public-strip: маркеры **выводятся из исходников** (чанки из App.tsx, команды из edition_owner-пакетов + имя identity) — 13 маркеров вместо 4 hand-typed | build-release.mjs | live-прогон по реальному бинарю |
| version.mjs: якорь на top-level ключ; .dockerignore: ключевые паттерны | scripts | node --check |
| Rust: exit-**watchdog спавнится до** shutdown-write — wedged-пайп больше не вешает выход приложения навсегда | sidecar.rs | cargo test 5/5 |
| X-Tunnel-Client-IP: инвариант прописан в коде | gateway.go | комментарий |
| ChainWizard: per-row loading (Set), стабильные id строк через move, фокус не прыгает | ModelRoutesPage + chain-entries.ts | 9 новых тестов |
| WorkspaceBoundary «Try again» протестирован | WorkspaceBoundary.test.tsx | 1 тест |
| #ffc9c3: комментарий о намеренном более светлом розовом | GuardrailsPage.module.css | — |

## 5. Матрица (финальная, после закрытия всех находок ревью)

- `go vet` + `-tags public`: pass; `go test ./...` **обоих изданий**: pass; `go test -race ./...`: pass (полное дерево); `gofmt -l`: пусто
- Фронтенд: oxlint 0/0, tsc, **132 теста** (29 файлов), production build — pass
- `cargo test`: 5/5; `pnpm version:check`: 1.0.29 согласована во всех 6 местах
- Мутационные проверки раунда: гейт `chatDialect` (убит тестом framing-утверждением), `anon`-дельты (убит), `.done`-суффикс (убит), re-roll ladder, deframed scan, migration re-run

## 8. Итог повторного ревью (4 зоны)

- **relay core**: 9/9 фиксов CONFIRMED FIXED; найдены 1 high (chatCompletionBody вне диалекта — **закрыт тройной защитой: гейт + честная метка + байтовый выбор диалекта в reviewResponse**) + 4 low + 1 info — все закрыты в этом же цикле
- **translation+guardrails**: .done/CR/anthropic-generation CONFIRMED; **найдена моя регрессия** — `anon_` на event-пути ломал аккумуляцию безымянных дельт (high, **закрыта** + 2 теста); toolSource-lookup и doc-комментарий закрыты
- **platform+release**: Diagnostics/TrimToPayloadBudget/atomicfile/dedup/release-маркеры/watchdog/live-e2e CONFIRMED; миграционный **anyExists-гейт отменял per-file восстановление (high, закрыт — гейт удалён)**; mojibake и substring-матч закрыты
- **frontend+keypool**: все четыре Zone A фикса CONFIRMED с пиннингом; keypool dedup «ровно одна копия» CONFIRMED; 1 medium (тест unreadable-rows) + 4 low — все закрыты (тест добавлен, %v в логе, тип ChainEntryPatch, .catch на discover, переименование stale-теста)

## 9. Остатки (осознанные, с обоснованием)

- **server.go один файл** — контракт сам запрещает дробить «ради строк» («Не дробить цельный код только ради метрики строк»); смешение ответственностей лечится экстракцией, не сплитом, и все швы уже покрыты точечными тестами.
- Провайдер-специфика в общем коде релея (aieva-путь, GLM-ранги, image default) — переезд в profiles требует доменных изменений, отложено.
- Triple-buffering стримов; `previous_response_id` (непереводим — документировано); hub shared-trust (документировано); Docker tag-pinning (не воспроизводится офлайн).
- `text.format` неизвестного типа тихо отбрасывается (вместо явной ошибки) — low.
- Гейт миграции тестируется на уровне хелпера (`importEachMissing`); полный вход `migrateLegacySettings` требует DPAPI-зашифрованного legacy-конфига — покрытие решением ревью признано достаточным.
- [info] ревью: `report()` в stdio держит `writeMu` на время записи (сериализация при не-threadsafe writer); cross-slice edge relay→keypool/domain (один контракт, циклов нет) — наблюдаемые, менять нечего.

## 10. Дифф

78 файлов, +2520/−441, версия 1.0.29, всё в рабочем дереве (не закоммичено — коммит за владельцем).
