# Ревью-цикл 2: фиксы + повторное полное ревью (Luxury Switchboard 1.0.28)

Продолжение [FULL-REVIEW.md](FULL-REVIEW.md): первый раунд нашёл 4 high, 14 medium
и ~35 low; этот цикл чинит **все high и medium плюс весь дешёвый low-пакет**, поднимает
патч 1.0.27 → 1.0.28, прогоняет полную матрицу проверок и повторяет независимое
ревью восемью свежими зонными ревьюерами — их находки тоже закрыты в этом же дереве.

---

## 1. Фиксы раунда 1 (все проверены повторным ревью, каждое — CONFIRMED FIXED)

### High
| # | Что | Где | Тест-пин |
|---|---|---|---|
| H1 | Сброс `bufferTerminal` (и entry-пути, и диалект-флага) на каждой guardrail re-roll попытке — раньше non-stream fallback протекал в re-roll, и сырой JSON писался в закоммиченный SSE без terminal event | relay/adapters/http/server.go | `TestARerolledStreamStillSpeaksTheStreamDialect` (мутация убита: без фикса клиент получает сырой JSON) |
| H2 | `history.recent` под байтовым бюджетом `withinOneFrame` + честный `available`; фронтенд несёт `recentAvailable` и показывает «Newest N of M rows» | activity stdio register.go, insights-порт/модель/страница | `TestTheHistoryTheWorkspaceAsksForFitInOneProtocolFrame`, `names a bounded row list as the newest part` |
| H3 | `providerRates` в `CheckPool` читается под `mu.RLock` (раньше — без лока: runtime-fatal, не ловимый `recover()`); `load()` снапшотит таблицу под локом | keypool checkpool.go, manager.go | `TestCheckPoolReadsTheProviderTableUnderTheSameLockAsItsWriters` (мутация убита под `-race`: DATA RACE пойман) |
| H4 | BackupPanel подписывается через `useSyncExternalStore` со стабильными модульными idle-фолбэками — feedback экспорта/импорта больше не застревает в модели | settings/SettingsPage.tsx | `shows the backup outcome as it happens` (мутация убита: «Writing…» не появляется) |

### Medium
| # | Что | Где |
|---|---|---|
| M1 | `finishLease` до switchRoute в congestion-ветке (один вызов на обе развилки; once-guard делает повтор no-op) | server.go; `TestTheCongestionFailoverAlsoFinishesItsLease` (мутация: 4/3) |
| M2 | `upstreamErrorDetail`: скалярная экстракция причины + **скраб эхо запроса** (`redactRequestEchoes`, ≥24 байт) + секреты, **до** rune-границы (усечение после скраба — иначе длинная цитата остаётся неусечённым префиксом) | server.go; `TestARequestQuotedBackInAnErrorDoesNotBecomeHistory` (мутация убита) |
| M3 | Tool-restore решает framing **по байтам** (`bodyLooksLikeEventStream`), не по лейблу; гейт читает тело только для ремонтопригодных ответов; oversize/broken-read **сплайсится обратно** (как в guardrail-слое), а не маскируется усечкой | tools_compat.go; `TestToolNamesAreRestoredWhateverTheAnswerIsLabelled` (4 комбинации лейбл/байты) |
| M4 | `stripEncryptedReasoning` сохраняет plaintext-сводку (удаляет только seal) — как README и обещал | tools_compat.go; тест дополнен assertion'ом summary |
| M5-M7 | Пробер `keys.check`: путь из `provider.ModelsPath` (общий `providers/domain.JoinCatalogPath`), auth-shape как у релея (auto+anthropic → x-api-key), egress через прокси ключа (bounded кэш клиентов) + **redirect = «нет ответа»** (последний клиент в дереве, который ходил по редиректам) | probe/prober.go, health/prober.go; 6 тестов |
| M8 | CI: push на main и pull_request запускают check-only job; сборка/артефакты только на теге/dispatch; **typecheck фронтенда в check-only** (иначе type-ошибка падала бы на релизе, а не на коммите); gofmt-шаг (Linux, до тестов); `vet -tags public`; concurrency group с event_name (dispatch больше не отменяется пушем) | .github/workflows/build-desktop.yml |
| M9 | stdio `Serve`: сканер в собственной горутине → select на `serveCtx.Done` — SIGTERM завершает Serve и drain при молчащем stdin | platform/stdio/server.go; `TestCancellationEndsServeWhileTheReaderIsQuiet` |
| M10 | Потери history видимы: `Store.OnDrop` (queue_full / write_failed, именованные константы) → notification `history_drop` с разными заголовками на причину; гонка shutdown не даёт ложный toast | activity sqlite store.go, bootstrap app.go, notifications domain |
| M11 | Plaintext-бэкап оформлен в контракте как принятый обмен | AGENTS.md |
| M12 | Auto-discovery в Model Routes: раз на провайдера (ref), не на каждую смену phase — цикл discover/abort невозможен | ModelRoutesPage.tsx |
| M13 | Mojibake U+FFFD → «…» (3 строки Chain Wizard) | ModelRoutesPage.tsx |
| M14 | CSS-токены: `--bg-field`, `--border-field`, `--text-danger`, `--accent-blue`; свип по 10 модулям (включая остатки в index.css, найденные повторным ревью) | index.css + module css |

### Low-пакет
`declareBetaFeature` мультистрочные `Anthropic-Beta` (Values/Add, пустые строки отфильтрованы — мутация убита); `sanitize.go` literal `4` → `relayapp.MinRedactableMarkerBytes`; AGENTS.md v6→v7; комментарий check-version.mjs; мёртвые якоря wiring.go; OverviewPage idle-check; `aria-label="Sidebar"`; **drift-тест лимита фрейма** (читает `MAX_FRAME_BYTES` из sidecar.rs, якорь на `;`); **редакция evidence guardrails** — `Subject.Secrets` → `redactEvidence` чистит Match/Excerpt/**Source** с порогом 8 байт, вердикт решается по настоящим байтам (инспекторные тесты + e2e с маркером ровно 8 байт = детектор дрейфа порогов).

---

## 2. Находки повторного ревью, закрытые в этом же дереве

Повторное ревью подтвердило все фиксы («CONFIRMED FIXED» в каждой из 8 зон) и нашло новое — всё закрыто:

1. **[guardrails, high] `Finding.Source` нёс имя tool'а от провайдера без редакции** — эхо-ключ как имя инструмента попадало в журнал и на экран через unsolicited-tool аномалию. → `Source` тоже чистится; тест маршалит запись целиком.
2. **[relay, medium] Скраб эхо выполнялся ПОСЛЕ 4096-рунной усечки** — длинная цитата оставалась неусеченным префиксом, который ReplaceAll уже не матчит. → `jsonErrorDetailUnbounded`/`terminalErrorDetailUnbounded`/`filedErrorDetail`: экстракция → секреты → эхо → усечение.
3. **[relay, medium] `restoreResponseToolCalls` молча усекал oversize/broken тела** — полный ответ превращался в «завершённый» частичный. → ранний return для непочинимых тел + splice-back через MultiReader (как guardrail-слой).
4. **[relay, medium] JSON-ответ под event-stream лейблом** — лейбл гнал в stream-repair, который ничего не находил и подавлял JSON-rewrite → алиасы провайдера у клиента. → framing решают байты, четвёртая комбинация в тесте.
5. **[relay, medium, pre-existing] Re-roll после mid-request chat-discovery слал Responses-тело на переписанный chat-путь** → снапшот entry-пути + сброс `chatActive` на re-roll — лестница повторяет вывод диалекта; `TestARerolledChatDiscoveryWalksTheSameLadder` (мутация: Responses-тело на chat-эндпоинте считается).
6. **[tunnel, medium] Метаданные изображений вне маркер-свипа** — PNG tEXt/JPEG COM несут model id. → декодированные байты проходят `redactor.contains` (fail-closed); тест с PNG+маркером (мутация убита).
7. **[tunnel, low] Эхо-скраб не на всех filing-путях** → все terminal-пути через `filedErrorDetail` (экстракция+скраб).
8. **[release, medium] check-only CI не типизировал фронтенд** → `pnpm --dir frontend typecheck` в Check source.
9. **[release, low] dispatch на main отменял push** → event_name в concurrency group; **[low] gofmt-шаг задвоен и последним** → Linux-only, первым; **[low] регекс без якоря** → `…(\d+)\s*;`.
10. **[frontend, low] остатки сырых hex в index.css, нестабильные idle-фолбэки, непиннированный маппинг `available`** → всё закрыто (+новый тест порта).
11. **[keypool, medium] Пробер ходил по redirect'ам** — единственный клиент в дереве; вердикт по цели редиректа + credential на чужой хост. → `useLastResponse` + 3xx = 0; тест: цель редиректа не получает ни одного хита.
12. **[keypool/platform, low] `JoinCatalogPath` без теста и с двойником в релее; список proxy-схем в трёх копиях; ложный queue_full при shutdown; общий ключ дедупа на две причины** → таблица-тест + именованные константы причин + `ProxySchemeAllowed` один раз в keypool/domain (используют relay/пробер/домен ключа) + re-check `closed` после отказа очереди.
13. **[translation, low] Responses→Chat теряет reasoning-сводки, о чём не сказано** → цена задокументирована в `inputItemsToChat` и закреплена тестом `TestTheChatTranslationDropsReasoningSummariesDeliberately` (маппинг — осознанное решение, не авария).
14. **[translation, low] Dispatch передавал переписанный путь в tool-restore для image-запросов** → restore получает `request.Path` вызывающего, инвариант в комментарии.

---

## 3. Матрица проверок (финальная, после всех фиксов)

- `go vet` + `go vet -tags public`: pass
- `go test ./...` (owner) и `-tags public`: pass
- `go test -race ./...` (полное дерево): pass
- `gofmt -l backend`: пусто
- `pnpm --dir frontend check` (oxlint + tsc + vitest + vite build): pass
- `cargo check`: pass
- `pnpm version:check`: 1.0.28 согласована во всех 6 местах; SHA256SUMS.txt не тронут (запись релиза 1.0.27)
- Каждый существенный фикс закрыт мутационной проверкой: временный откат → тест краснеет → фикс возвращается (см. историю сессии)

## 4. Что осталось (осознанные остатки, не дефекты релиза)

По каждому решено «не чинить сейчас» с обоснованием в отчётах раунда 1/2:

- **Relay**: provider-специфика в общем коде (aieva-путь, GLM-ранги, `gpt-5.6-sol`) — переезд в profiles; server.go держит ~5 ответственностей (швы чистые); guardrail re-roll wait-отмена без terminal event; triple-buffering стримов; fast-path scan framing-чувствительность; Responses→Chat теряет `text.format`/`previous_response_id` (у chat нет эквивалента последнему); images теряет `n`.
- **Guardrails**: `.done`-события дублируют payload в аккумуляторе; CR-only SSE; unnamed items в одном аккумуляторе — все low, атакующей ценности с учёта закрытых векторов почти нет.
- **Tunnel**: X-Tunnel-Client-IP доверяется за Caddy-перезаписью (инвариант закреплён тестом шаблона); LoadProfiles тихо дропает битые строки; hub shared-trust; gate-oracle на bridge — все документированные trade-offs.
- **Keypool**: duplicate key ID на load лочит пул (dormant — builtins=nil).
- **Platform**: prices.go без fsync; Emit глотает ошибки; migration не crash-atomic; четвёртая копия withinOneFrame — кандидат в platform helper.

## 5. Дифф

60 файлов, +1601/−206 строк, версия 1.0.28, рабочее дерево содержит все изменения (не закоммичено — коммит за владельцем).
