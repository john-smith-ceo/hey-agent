# Аудит: SPEC-HEY-AGENT.md (контракт v2) против реализации

Дата: 28.09.2026. Worktree: `~/Projects/global/hey-agent-dev`
(ветка `vlad2141950/joh-348-…`, база — `main` @ `729cbe9` = hey-agent 0.1.3).
Живой checkout `~/Projects/global/hey-agent` использован только для чтения.

Аудируемая спека — `SPEC-HEY-AGENT.md` в редакции «контракт v2 — единый
voice-daemon» (JOH-348 v2, решения PO от 28.09.2026). Текущий код реализует
v1-архитектуру: foreground-слушатель в скрытом tmux-окне, без daemon'а,
сокета, TTS-вывода и трея. Поэтому большинство пунктов v2 в коде отсутствуют —
это ожидаемое состояние до реализации JOH-348, а не регресс.

## 1. Таблица аудита: спека против кода

| Пункт спеки | Статус в коде (файл:строка) | Расхождение | Канон v2 |
|---|---|---|---|
| §1 Ввод: хоткей → ffmpeg → STT → панель | Реализовано: `cmd/hey-agent/main.go:615` (listen), `:420` (run), `internal/bridge/bridge.go:152-242`, `internal/record/ffmpeg.go:35`, `internal/transcribe/openai.go:107`, `internal/tmux/sender.go:50` | Цель фиксируется один раз при `listen`, а не «актуальная» на момент записи | Сохранить тракт; цель — по §3 |
| §1 Вывод: TTS `speak`/`hush` из agent-voice-over | Отсутствует: нет `internal/voiceout`, нет команд `speak`/`hush`, нет ffplay | Не реализовано | Перенос agent-voice-over внутрь hey-agent |
| §1 Индикация: tmux status-right + tray | Частично: только status-right, один индикатор `internal/tmux/status.go:127-143` | Нет раздельных `in:`/`out:`-сегментов; tray отсутствует | Два сегмента + StatusNotifierItem |
| §1 Процесс: daemon под systemd user / `hey-agent daemon` | Отсутствует: надзор — скрытое tmux-окно `hey-agent-voice` (`main.go:29`, `main.go:664-687`); systemd-юнита в `packaging/` нет | Архитектура v1; окно `hey-agent-voice` по v2 подлежит удалению | Daemon + systemd; «foreground-listener в tmux-окне» — отменено решением PO 28.09.2026 |
| §2 Unix socket `$XDG_RUNTIME_DIR/hey-agent.sock` (fallback `/tmp/hey-agent-$UID.sock`), NDJSON-протокол | Отсутствует полностью: нет socket-кода; межпроцессный канал — JSON-файл `/tmp/hey-agent-<sha>.json` (`main.go:571-574`) + поллинг `bridge.go:262-305` | Нужен новый транспорт; файл-конфиг — временная замена `config` | Socket 0600, ответ ≤2 с |
| §2 CLI `status|bind|unbind|speak|hush|config|stop` как тонкие клиенты | Есть только `config` и `stop` (`main.go:77-88`); `config` пишет файл, не ходит в сокет (`main.go:502-559`); `status`/`bind`/`unbind`/`speak`/`hush` отсутствуют | Набор команд не совпадает | Все команды — клиенты сокета |
| §2 `listen` — совместимая команда (поднимает/присоединяет daemon) | `listen` создаёт скрытое tmux-окно и запускает `run --attached` (`main.go:678-687`) | Механика другая: не daemon, а окно-супервизор | `listen` → daemon client |
| §3 Цель «актуальная», резолвер до записи и перед отправкой | Отсутствует: цель = `--target` или `$TMUX_PANE` на старте (`main.go:619`, `main.go:651-658`); проверка панели — один раз в `bridge.New` → `sender.Check` (`bridge.go:109-111`, `sender.go:42-48`) | Нет per-recording резолва, нет повторной проверки, нет fail-closed матрицы | Fail-closed: смена цели/нет клиента/неоднозначность → дроп + error |
| §3 `--target` как explicit override | Реализовано (`main.go:619`), проверка существования панели есть | Проверки «на существование» достаточно; поведение совместимо | Остаётся |
| §3 `--submit` opt-in | Реализовано (`main.go:624`, `bridge.go:55`, `sender.go:67-82`; задержка `tmux.DefaultSubmitDelay` `sender.go:18`) | Соответствует | Остаётся opt-in |
| §4 FSM `Idle→Recording→Transcribing→Delivering→Idle` | Частично: состояния `idle/recording/transcribing/pasted/error` рассылаются строками `b.state()` (`bridge.go:191,216,239-240,330-331`); явного FSM-типа нет | `Delivering` не выделен; состояния — строки, не enum | Явный FSM + состояние `Speaking` |
| §4 `Idle→Speaking→Idle`, взаимоисключение Recording ⊥ Speaking | Отсутствует: состояния `Speaking` нет; глушение сейчас внешнее — busy-file/on-record (`bridge.go:337-362`) | Петлю глушит внешний файл, не внутренний мьютекс | Внутренний мьютекс состояний; busy-file — только публикация наружу |
| §4 `speak` при записи — очередь глубиной 1 | Отсутствует | Не реализовано | Очередь 1, новый speak заменяет ожидающий |
| §4 Хоткей во время Speaking обрывает вывод | Отсутствует | Не реализовано | Требуется |
| §4 `Escape` → Idle без транскрибации/доставки | Реализовано: `bridge.go:153-157` → `stop(false)` `:307-324`; Cancel эмитится гейтом `gate.go:44-46`; адаптеры `xrecord_linux.go:31-32`, `tap_darwin.go:35-36`; тесты `gate_test.go:86`, `bridge_test.go:36,65` | Соответствует | Сохранить |
| §4 busy-file/on-record-совместимость | Реализовано: `bridge.go:337-362`; env `HEY_AGENT_BUSY_FILE`, `HEY_AGENT_ON_RECORD` (`main.go:425-426`, `main.go:622-623`) | По v2 остаётся как внешняя публикация; `on-record` после переноса TTS теряет смысл — вопрос к PO | Busy-file — да; on-record — под вопросом |
| §5 Транскрибация: OpenAI-compatible, multipart file+model, timeout 60 с, ошибки без тела | Реализовано: `openai.go:18-22` (дефолты, `RequestTimeout = 60s`), `:118-127` (multipart), `:146-148` (статус без тела); env `HEY_AGENT_BASE_URL/MODEL/API_KEY` (`main.go:237-247`, `:472`) | Соответствует; нюанс: дефолт 60 с тестом не зафиксирован (тест проверяет boundedness с custom timeout, `openai_test.go:135-153`) | Без изменений; добавить тест на дефолт 60 с |
| §5 TTS: `HEY_AGENT_TTS_*`, `/audio/speech`, ffplay, hush убивает плеер | Отсутствует: env `HEY_AGENT_TTS_*` нигде не читаются; ffplay не вызывается | Не реализовано | Требуется |
| §6 tmux status-right: сегменты `in:`/`out:`, состояние из socket `status` | Частично: park/restore реализован полностью (`status.go:55-198`, опции `@hey-agent-status-was/-base/-length-was/-base` `:19-23`); индикатор один (`status.go:127-143`), без разделения in/out | Park/restore — канон и сохраняется; сегментации нет | `in:idle|rec…|transcribe…|err`, `out:idle|speak…|err` |
| §6 Tray: StatusNotifierItem по session D-Bus через `godbus/dbus`, меню, иконка по FSM | Отсутствует: `go.mod` без зависимостей вообще (`go.mod:1-3`), D-Bus-кода нет | Не реализовано; старое правило «no tray» — **отменено решением PO 28.09.2026** | SNI + меню из спеки; headless → работа без иконки |
| §7 Канон секрета `~/Tools/linux/hey-agent/.env` (0600), `HEY_AGENT_ENV_FILE` | Частично: файл `.env` с 0600 и каталогом 0700 реализован (`main.go:321-337`, тест `main_test.go:9-24`), `HEY_AGENT_ENV_FILE` honoured (`main.go:311-313`) | **Путь другой**: код пишет в `~/Projects/tools/hey-agent/.env` (`main.go:314-318`), спека требует `~/Tools/linux/hey-agent/.env` | Миграция пути — вопрос к PO |
| §7 Сокет 0600, payload без путей/shell | n/a — сокета нет | Не реализовано | Требуется |
| §7 Временные wav/mp3 удаляются при любом исходе | Реализовано: `bridge.go:210` (`defer os.Remove`), `ffmpeg.go:44-47,104-110` (ошибки), отмена через ctx (`ffmpeg.go:96-99`) | Соответствует | Сохранить; добавить mp3 при TTS |
| §7 Daemon пишет только в резолвленную цель | Частично: sender пишет в фиксированный `--target`, пустой отказан (`sender.go:30-38`, `:51-53`) | «Резолвленная цель» появится с §3 | Требуется резолвер |
| §8 Миграция: dev-ветка, `dev-bin/hey-agent` | Частично: worktree на месте, `dev-bin/hey-agent` собран (ELF 9 641 672 байт); `internal/voiceout` отсутствует; `docs/removal-list.md` отсутствует | Перенос agent-voice-over не начат | Cutover только по решению PO; откат — по `docs/snapshot-2026-09-28.md` |
| §9 Unit-тесты: socket, резолвер, FSM, fail-closed, tray-маппинг, dotenv | Частично: dotenv-тесты есть (`main_test.go:9-52`); socket/resolver/FSM/tray-тестов нет (кода нет) | Покрытие v1-скоупа; v2-скоуп не начат | Дописать вместе с кодом |
| §9 Интеграция на стенде (JOH-354): `tmux -L heydev`, Xvfb `:99`, httptest-мок | Заготовка есть: `scripts/mock-provider.sh` (python3-мок `/models` + `/audio/transcriptions`); stand-up/down и сценариев нет; в Go-тестах httptest не используется — `roundTripFunc` (`openai_test.go:16-18`) | Стенд не поднят | Отдельная задача JOH-354 |
| §9 `go test ./...` и `go vet ./...` зелёные | Не проверялось в рамках аудита (запуск сборок вне worktree-кэшей ограничен); тесты v1 на месте | Требуется прогон перед отчётом по JOH-348 | Обязательное условие |
| v1-спека: «без daemon, launch agent, systemd, tray, autostart» (REBUILD-PLAN §2, старый SPEC UI scope) | В коде соблюдено: один foreground-бинарь, надзор — tmux-окно | Правило больше не действует | **Отменено решением PO 28.09.2026** (daemon + tray теперь канон) |
| v1-спека: «No automatic reading of agent output; belongs to agent-voice-over» (REBUILD-PLAN §8) | Соблюдено: вывода нет | Правило отменено | **Отменено решением PO 28.09.2026** — voice output переезжает внутрь |
| v1-спека: цель = явный `--target` / текущая панель | Реализовано (`main.go:619,651-658`) | Заменено семантикой «актуальная панель» | **Изменено решением PO 28.09.2026** — fail-closed resolver |
| v1-спека: «listener supervised by hidden tmux window» (старый SPEC, medium-2) | Реализовано (`main.go:29,664-687`) | Заменено systemd-надзором | **Отменено решением PO 28.09.2026** |

### Замечания к покрытию тестовой спеки (v1-раздел SPEC/плана)

| Требование | Статус | Расхождение |
|---|---|---|
| `Escape` → только `Cancel` в адаптерах macOS/X11 | Гейт покрыт `gate_test.go:86`; адаптеры мапят Escape→cancel (`xrecord_linux.go:31-32`, `tap_darwin.go:35-36`) без платформенных тестов | Приемлемо: платформенная часть тестируется стендом |
| Отмена → idle, без транскрибации | `bridge_test.go:36,65` | Покрыто |
| dotenv save/load, 0600, empty-reject, key-name parsing | `main_test.go:9-52` | Покрыто |
| Provider: custom base URL/model, multipart, 60-с default, redacted errors | `openai_test.go:30,85,100,135` | Нет теста, что **дефолт** = 60 с (проверяется только boundedness) |
| Empty transcription rejection + submit opt-in | opt-in покрыт `sender_test.go:11,41`; rejection реализован (`sender.go:51-53`, `openai.go:155-157`), теста нет | Добавить тест |
| Restore tmux status после stop и после смерти окна | Реализовано (`status.go:147-198`, `main.go:700-746`); теста нет | Добавить тест/стенд-кейс |
| httptest-зависимость в сьюте | Устранено: `roundTripFunc`, httptest в коде нет | Пункт старой спеки закрыт |

## 2. Проверка ребилда hey-agent (JOH-335): остатки старых имён

Поиск `hey-claude|hey-codex|hey-claudex|jarvis-voice` (+ варианты в env/регистре)
по worktree и чтением по `~/Projects`. Проверены: Go-код, Makefile, packaging/,
plugins/, `.agents/`, README, go.mod, env-имена `HEY_AGENT_*`, tmux-опции,
префиксы временных файлов, CI-workflows.

### Что переименовано корректно

- Модуль `github.com/john-smith-ceo/hey-agent` (`go.mod:1`), каталог `cmd/hey-agent`.
- `installNames = ["hey-agent"]` — алиасов нет (`main.go:34`).
- Env: `HEY_AGENT_API_KEY/BASE_URL/MODEL/ENV_FILE/BUSY_FILE/ON_RECORD` (`main.go`).
- tmux-опции `@hey-agent-*` (`status.go:19-23`), окно `hey-agent-voice` (`main.go:29`).
- Временные файлы: `hey-agent-*.wav` (`ffmpeg.go:39`), `/tmp/hey-agent-<sha>.json` (`main.go:571-574`), `.hey-agent-config-*` (`main.go:593`), tmux-буфер `hey-agent-*` (`sender.go:91`).
- Makefile, CI-workflows, `packaging/` имя файла — чисто.

### Найденные остатки

| Место | Что там | Нужно ли менять |
|---|---|---|
| `plugins/hey-codex/.codex-plugin/plugin.json:2,12,16` | Плагин `"name": "hey-codex"`, displayName «Hey Codex», defaultPrompt про hey-codex | Да — план §3 требует убрать алиасы; переименовать плагин или удалить каталог |
| `plugins/hey-codex/skills/hey-codex/SKILL.md:2-3,12-13,18-22` | Команды `hey-codex --submit`, `hey-claudex stop`, репо `hey-claudex`; инструкция запускает бинарь, которого больше нет | Да — переписать на `hey-agent listen/stop` или удалить |
| `.agents/plugins/marketplace.json:2,8,11` | Маркетплейс `"name": "hey-claudex"`, plugin `hey-codex` | Да — переименовать/удалить вместе с плагином |
| `packaging/homebrew/hey-agent.rb.tmpl:1-2` | `class HeyCodex < Formula`, desc «Voice input for Codex in tmux» — имя файла новое, содержимое старое | Да — `HeyAgent`, нейтральный desc |
| `internal/tmux/sender_test.go:64,66,73-74` | Тестовые env `HEY_CODEX_TMUX_LOG`, `HEY_CODEX_TMUX_STDIN` | Желательно — косметика, но это env-имена с legacy-префиксом; переименовать в `HEY_AGENT_TMUX_*` |
| `README.md:17-32` | Раздел «Codex plugin»: marketplace `john-smith-ceo/hey-claudex`, `hey-codex@hey-claudex`, `hey-claudex stop` | Да — раздел противоречит плану §3/§8 («no model-specific aliases»); удалить или переписать после судьбы плагина |
| `README.md:164-165`, `cmd/hey-agent/main.go:156-159` | Примеры `--busy-file ~/.config/jarvis-voice/mic-busy`, `--on-record "jarvis-voice hush"` | После cutover: заменить на `hey-agent`-средства (внутренний мьютекс по v2 §4). Сейчас — рабочий пример с живым `agent-voice-over`, не трогать до переноса |
| `cmd/hey-agent/main.go:374-375` | Комментарий «links the built binary under all three names. The aliases are not decoration…» — код давно одноимённый | Да — протухший комментарий эпохи алиасов |
| `cmd/hey-agent/main.go:36-48` | Мёртвый код: `expectedApp()` возвращает `""`, `isOwnName()` нигде не вызывается | Да — удалить |
| `internal/secret/` (весь пакет) | Keychain/`~/.config/hey-agent` хранилище; **ни один файл не импортирует пакет** — мёртвый код эпохи до `.env`; на darwin `doctor` всё ещё требует бинарь `security` (`main.go:217-219`) | Да — удалить пакет и проверку `security` из doctor |
| `docs/snapshot-2026-09-28.md` | `jarvis-voice`, `run --attached` в описании живого контура | Нет — исторический снимок, канон для отката |
| Живой процесс (см. snapshot) | Слушатель работает как `hey-agent run --attached …` | **Расхождение зафиксировано**: команда `run` есть в коде (`main.go:89-90`, `:420-494`), но не описана ни в README, ни в `usage()` (`main.go:100-170`); заодно в `usage()` нет `install`. Решить: документировать или сделать внутренней |
| `~/Projects/global/agents-template/agents/claude.md:12` | «Голосовой ввод в твоей панели tmux: `hey-claude`» | Да — вне репозитория, но это живой указатель для агентов |
| `~/Projects/global/mission-control/SUMMARY.md:94` | Упоминание хуков `jarvis-voice-*` | Нет/позже — описание чужого проекта, резолвится cutover'ом agent-voice-over |
| `~/Projects/global/agent-voice-over/` | `~/.config/jarvis-voice` пути (`cmd/agent-voice-over/main.go:365,374`), `jarvis-voice` в `docs/cleanup-plan.md`, «ключ общий с hey-claudex» в `profiles/onyx-v1.json:14` | Позже — это переносимый проект; имена уйдут при слиянии в `internal/voiceout` |
| `~/Projects/global/sir-john-shell/AGENTS.md:68` | «`hey-agent` must not be added to this repository» | Нет — это запрет, не остаток |
| Бинарь `dev-bin/hey-agent` | Собран 28.09 из ветки; по коду старых публичных имён нет | Проверить `strings` после финальной сборки — acceptance плана §7 |
| `~/.local/bin/hey-agent` | Symlink на живой `bin/hey-agent` (snapshot:16) | Не трогаем до cutover (не проверялся — вне скоупа) |

### Прочие расхождения ребилда

- **`run` не задокументирован**: есть в диспетчере (`main.go:89`) и это фактический
  режим слушателя (spawn из `listen`, `main.go:678-682`), но отсутствует в README
  и в `usage()`. Живой процесс запущен именно `run --attached` (snapshot:20-24).
- **Неявный listen**: голый `hey-agent` и `hey-agent --flag` запускают `listen`
  (`main.go:60-72`) — план §2 требует «explicit command dispatch» и «ничего не
  захватывать до явного listen». UX-решение задокументировать или убрать.
- **Путь канонического .env**: код `~/Projects/tools/hey-agent/.env`
  (`main.go:314-318`) против спеки v2 `~/Tools/linux/hey-agent/.env` (§7) и
  README `tools/hey-agent/.env` (README:61,79). Три разных описания одного файла.
- **doctor на darwin** требует `security` (`main.go:217-219`) — остаток keychain-эры,
  секрет теперь только в `.env`.
- **CI только macOS**: все три workflow (`ci.yml:9`, `test.yml:18`, `release.yml:18-27`)
  собирают/тестируют только darwin; Linux/X11-тракт (основной на машине PO)
  не гоняется. Для v2 с D-Bus-треем Linux-джоба обязательна.
- **Версия зашита**: `const version = "0.1.3"` (`main.go:31`) — для v2 нужен
  план бампа (минор/мажор).

## 3. Открытые вопросы к PO

1. **Статус команды `run`**: оставить публичной и задокументировать в README/usage,
   или сделать внутренней (убрать из диспетчера help, вызывать только из `listen`)?
   Сейчас это единственный реальный режим слушателя, но пользователь о нём не знает.
2. **Неявный `listen`** на голый вызов и на флаги (`main.go:60-72`) — осознанный UX
   («единственная команда, которая нужна») или нарушение explicit-dispatch из плана §2?
3. **Канонический путь .env**: мигрируем `~/Projects/tools/hey-agent/.env` →
   `~/Tools/linux/hey-agent/.env`? Что делать с уже сохранёнными ключами —
   авто-миграция при `doctor`/`listen` или только документация?
4. **Судьба Codex-плагина**: `plugins/hey-codex` + `.agents/marketplace hey-claudex`
   + раздел в README. Удаляем сейчас (план §3 «убрать алиасы») или переименовываем
   в `hey-agent`-плагин в рамках v2? В v2-спеке про плагин ни слова.
5. **Мёртвый код**: подтвердить удаление `internal/secret/` (keychain),
   `expectedApp`/`isOwnName`, проверки `security` в doctor.
6. **Env-алиасы ключа**: `loadDotenvKey` принимает `OPENAI_API_KEY` и
   `OPEN_AI_API_KEY` (`main.go:368-372`, задокументировано README:65-66).
   В v2 §5 перечислен только `HEY_AGENT_API_KEY` — алиасы оставить?
7. **`HEY_AGENT_ON_RECORD`**: после переноса TTS внутрь глушение делает внутренний
   мьютекс (v2 §4). Флаг/env оставить для внешних чтецов или объявить deprecated?
8. **Тестовые env `HEY_CODEX_TMUX_*`** в `sender_test.go` — переименовать в
   `HEY_AGENT_TMUX_*` вместе с чисткой (нетехнический, но acceptance §7 плана
   «бинарь/репо без старых имён» читается широко)?
9. **Homebrew formula**: переименовать класс `HeyCodex`→`HeyAgent` и desc — это
   ломает `brew upgrade` для существующих установок; принять или держать legacy-имя
   формулы при новом бинаре?
10. **Тестовые пробелы, требующие решения о блокировке релиза**: нет тестов на
    empty-transcription rejection, на restore tmux status, на дефолтный timeout 60 с.
    Закрывать до v2 или в рамках v2-контракта (§9)?
11. **Linux CI**: добавить ubuntu-раннер с `libx11-dev libxtst-dev` (CGO) — без него
    v2-трей и XRecord ничем не проверяются.
12. Из спеки §10 уже открыты: enable systemd-юнита только на cutover; финальный
    состав tray-меню; TTS — общий `HEY_AGENT_API_KEY` или отдельный ключ.

---

*Аудит выполнен read-only по живому контуру; ни один бинарь hey-agent не
запускался; `.env`-файлы не читались; tmux-сессии и `~/.local/bin` не трогались.*
