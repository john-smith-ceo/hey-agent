# Removal-list: legacy-имена для cutover на `hey-agent`

Дата: 2026-09-28. Снято read-only (find/ls/grep/stat/systemctl list).
Ничего не изменено. Секреты (`.env`, `.secret`, `openai-api-key`) не читались —
фиксировались только имена файлов и пути.

Искомые имена: `hey-claude`, `hey-codex`, `hey-claudex`, `agent-voice-over`,
`jarvis-voice`, `hey-voice` и производные (пути, unit-имена, опции, tmux-окна,
env-переменные).

Связанные документы: `docs/voiceover-features.md` (инвентаризация a-v-o),
`docs/audit-spec-vs-impl.md`, `docs/snapshot-2026-09-28.md`.

Легенда действий: **удалить** / **заменить** (содержимое или ссылку) /
**переименовать** / **оставить** / **архив** (frozen, не трогаем при cutover).

## 1. Симлинки и бинарники

| Путь/место | Что там | Тип | Действие | Примечание |
|---|---|---|---|---|
| `~/.local/bin/agent-voice-over` | ELF-бинарь 8.6 МБ, живой daemon (pid 1248) | file | удалить | Заменяется `hey-agent speak/hush` (SPEC §1); сначала `systemctl --user stop/disable agent-voice-over.service` |
| `~/.local/bin/hey-claudex` | symlink → `~/Projects/hey-claudex/bin/hey-claudex` | symlink | удалить | **БИТЫЙ**: репо переехал в `~/Projects/frozen/hey-claudex` |
| `~/.local/bin/hey-claudex.pre-meta-r-20260902` | ELF-бэкап 9.6 МБ | file | удалить | Ручной бэкап бинаря hey-claudex |
| `~/.local/bin/hey-codex.bak-20260831` | ELF-бэкап 6.4 МБ | file | удалить | Ручной бэкап бинаря hey-codex |
| `~/.local/bin/hey-agent` | symlink → `~/Projects/global/hey-agent/bin/hey-agent` | symlink | оставить | Каноническое имя; живой |
| `~/Projects/frozen/hey-claudex/bin/hey-claudex`, `bin/hey-codex` | ELF-бинари в frozen-репо | file | архив | Часть замороженного репозитория |
| `~/Projects/frozen/archive/jarvis-voiceover/bin/jarvis-say`, `jarvis-voice`, `jarvis-voiced` | Python/Bash-скрипты старого стека | file | архив | Архивный репо-предшественник a-v-o |
| `~/Projects/frozen/codex-voice/bin/codex-voice` | Python-скрипт (TTS codex) | file | архив | Отдельный frozen-проект |
| `~/Projects/frozen/jarvis-ear/bin/jarvis-ear` | скрипт, использует `~/.config/jarvis-voice/mic-busy` и `jarvis-voice hush` (строки 38, 139) | file | архив | frozen; при желании починить — ждёт несуществующий `jarvis-voice` |
| `~/Projects/global/agent-voice-over/agent-voice-over` | ELF-бинарь 12.4 МБ в корне репо | file | удалить вместе с репо | Вопрос PO: репо `global/agent-voice-over` целиком уходит в frozen при cutover |
| `~/Projects/global/hey-agent-dev/dev-bin/hey-agent` | dev-бинарь, в help зашиты `jarvis-voice` строки | file | оставить/пересобрать | Пересоберётся после правки `main.go` |
| `~/.local/bin/jarvis`, `jarvis-agentd`, `jarvis-hook`, `jarvis-hub` | ELF-бинари (18.08) | file | решать отдельно | Семейство `the-jarvis`, НЕ входит в список имён cutover; вынесено сюда для полноты |
| `~/.local/bin/hey-misti`, `hey-mistral`, `hey-vibe` → `~/Projects/hey-misti/bin/hey-misti` | symlinks | symlink | решать отдельно | Смежная семья `hey-*`, вне scope |

## 2. Конфиги и env-пути

| Путь/место | Что там | Тип | Действие | Примечание |
|---|---|---|---|---|
| `~/.config/jarvis-voice/` | **пустой** каталог; сюда `hey-agent` пишет `mic-busy` на время записи (флаг `--busy-file`) | config dir | переименовать | Имя legacy, но путь ЖИВОЙ: используется в `~/Agents/rules/*.md`, `hey-agent/cmd/.../main.go:156`, работающем слушателе (snapshot §Процессы). Новое имя — вопрос PO (SPEC-HEY-AGENT.md:125) |
| `~/.config/agent-voice-over/` | `enabled`, `profile` (cedar), `.secret` (0600), `openai-voices/`, `lexicon.txt` + `lexicon-en.txt` | config dir | удалить/мигрировать | Активный конфиг a-v-o; `lexicon*.txt` — **БИТЫЕ** symlinks → `~/Projects/agent-voice-over/lexicons/` (репо в `global/`); `.secret` — источник OpenAI-ключа, перенести по новой схеме hey-agent |
| `~/.config/agent-voice/` | `mode`, `profile`, `openai-voices/`, `lexicon*.txt` | config dir | удалить | Конфиг предшественника `agent-voice`; lexicons — битые symlinks |
| `~/.config/hey-claudex/` | `openai-api-key` symlink → `~/Projects/tools/hey-claudex/openai-api-key` | config dir | удалить | **БИТЫЙ** symlink (`~/Projects/tools` не существует); путь был зашит в бинарь hey-claudex |
| `~/.config/jarvis-ear/` | `config.json`, `last-target`, `names.txt`, `refine.txt`/`terms.txt` symlinks → `~/Projects/jarvis-ear/…` | config dir | решать отдельно | Смежный проект (frozen); symlinks битые |
| `~/.config/autostart.disabled/jarvis-ear.desktop` | desktop-файл, `Exec=~/Projects/jarvis-ear/bin/jarvis-ear` | config | решать отдельно | Уже disabled; Exec-путь не существует |
| `~/.local/share/agent-voice-over/` | `agent-voice.sock` (0600, живой), `cache-go/`, `profiles` → `~/Projects/global/agent-voice-over/profiles` | data dir | удалить | Данные демона; `profiles`-ссылка живая |
| `~/.local/share/agent-voice/` | `agent-voice.sock` (stale), `cache-go/`, `profiles` → `~/Projects/agent-voice-over/profiles` | data dir | удалить | Legacy-данные; `profiles` **БИТАЯ** |
| `~/.local/share/jarvis/hub.db`, `~/.local/share/jarvis-ear/`, `~/.local/state/jarvis` | БД/логи/venv jarvis-семейства | data dir | решать отдельно | Вне списка имён cutover |
| `~/.local/share/piper/voices` | Голосовые модели Piper | data dir | оставить | Общий ресурс TTS |
| env-имена в a-v-o: `AGENT_VOICE_CONFIG` → `~/.config/agent-voice-over` → fallback `~/.config/jarvis-voice`; `AGENT_VOICE_HOME` → `~/.local/share/agent-voice-over` → fallback `~/.local/share/jarvis-voice`; `AGENT_VOICE_PLAYER`; `HEY_AGENT_BUSY_FILE` | резолвер каталогов с legacy-fallback (`main.go:356-380`) | config/code | заменить | См. voiceover-features.md #25; новая схема имён — `HEY_AGENT_*` |
| `EnvironmentFile=-%h/Projects/tools/openai/.env` в юните a-v-o | путь env-файла | unit option | удалить вместе с юнитом | Путь **не существует** (`~/Projects/tools` нет); демон живёт на `.secret` |

## 3. systemd (user)

| Путь/место | Что там | Тип | Действие | Примечание |
|---|---|---|---|---|
| `~/.config/systemd/user/agent-voice-over.service` | обычный файл (копия, 287 Б); `ExecStart=%h/.local/bin/agent-voice-over daemon` | unit file | удалить (после stop/disable) | enabled + active + running |
| `~/.config/systemd/user/default.target.wants/agent-voice-over.service` | symlink enable → `../agent-voice-over.service` | symlink | удалить | Снимается `systemctl --user disable` |
| `~/.config/systemd/user/agent-voice-over-tray.service` | symlink → `~/Projects/agent-voice-over/systemd/agent-voice-over-tray.service` | symlink | удалить | **БИТЫЙ**; юнит в состоянии `bad/enabled` |
| `~/.config/systemd/user/graphical-session.target.wants/agent-voice-over-tray.service` | symlink enable → `../agent-voice-over-tray.service` | symlink | удалить | Висит на битом юните |
| `~/Projects/global/agent-voice-over/systemd/agent-voice-over{,-tray}.service` | эталонные unit-файлы в репо | file | архив/удалить с репо | Копия в `~/.config/systemd/user` расходилась с репо |
| `~/Projects/global/agent-voice-over/launchd/com.agent-voice-over.tray.plist` | macOS-вариант службы | file | архив | Linux-машина; для полноты |
| `~/Projects/frozen/archive/jarvis-voiceover/systemd/{agent-voice-over.service,jarvis-voiced.service}` | unit-файлы архива; `jarvis-voiced` ExecStart → `~/.local/bin/jarvis-voiced`, Documentation → `~/.claude/skills/agent-voice-over/SKILL.md` | file | архив | На машине не установлены |

## 4. Плагины / skills / marketplace

| Путь/место | Что там | Тип | Действие | Примечание |
|---|---|---|---|---|
| `~/.codex/config.toml:12-15` | `[marketplaces.hey-claudex]` git → `github.com/john-smith-ceo/hey-claudex.git` | config | заменить | Переключить на marketplace hey-agent или удалить |
| `~/.codex/config.toml:47` | `[plugins."hey-codex@hey-claudex"] enabled = true` | config | заменить | Плагин переименуется вместе с `plugins/hey-codex` |
| `~/.codex/config.toml:115` | `[projects."/home/vlad/Projects/the-jarvis"]` trust entry | config | удалить | Каталог не существует — stale entry |
| `~/.codex/.tmp/marketplaces/hey-claudex/` | полный клон репо (rev 729cbe9): `plugins/hey-codex`, `cmd/hey-agent`, `SPEC/REBUILD-PLAN-HEY-AGENT.md`, `.codex-marketplace-install.json` | cache | удалить | Локальный кэш marketplace; пересоздастся под новым именем |
| `~/.codex/plugins/cache/hey-claudex/hey-codex/0.1.0/` | установленный плагин (`plugin.json`, `skills/hey-codex`) | cache | удалить | Кэш codex plugins |
| `~/.codex/skills/codex-voice` | symlink → `~/Projects/codex-voice/skills/codex-voice` | symlink | удалить | **БИТЫЙ** (репо в `frozen/codex-voice`) |
| `~/.codex/skills/jarvis-recall/SKILL.md` | skill «вспомнить роль Джарвиса» | file | оставить | Про идентичность, не про voice-пайплайн |
| `~/.config/devin/skills/hey-claudex/SKILL.md` | skill `/hey-claudex`; ссылается на `hey-claude`/`hey-codex`/`hey-claudex`, `jarvis-voice hush`, `~/.config/jarvis-voice/mic-busy`, окно `hey-claudex-voice` и **несуществующий** `~/Projects/global/hey-claudex` | file | заменить/переименовать | Переписать под `hey-agent` (`/hey-agent`?) |
| `~/.claude/skills/agent-voice-over`, `~/.claude/skills/agent-voice-off` | symlinks → `~/Projects/agent-voice-over/skills/…` | symlink | заменить | **БИТЫЕ**; пересадить на новые skill-файлы hey-agent |
| `~/Projects/global/hey-agent/plugins/hey-codex/` | `.codex-plugin/plugin.json` (name `hey-codex`, displayName `Hey Codex`, команда `/hey-codex`), `skills/hey-codex/SKILL.md` (ссылается на бинарь `hey-codex` и `hey-claudex stop`) | plugin | переименовать | Плагин внутри канонического репо носит старое имя |
| `~/Projects/global/hey-agent/.agents/plugins/marketplace.json` | marketplace `"name": "hey-claudex"`, displayName `Hey Claudex` | config | переименовать | То же в `hey-agent-dev` и в `frozen/hey-claudex` |
| `~/Projects/global/hey-agent-dev/plugins/hey-codex/`, `…/.agents/plugins/marketplace.json` | дубли вышеуказанного в dev-репо | plugin/config | переименовать | Править синхронно с `hey-agent` |
| `~/Projects/global/agent-voice-over/skills/agent-voice-over/`, `agent-voice-off/` | SKILL.md — источники битых `~/.claude/skills/*` | skill files | архив с репо | Переписать под `hey-agent speak/hush` |
| `~/Projects/frozen/codex-voice/skills/codex-voice/SKILL.md` | skill замороженного проекта | file | архив | — |

## 5. Документация и инструкции агентов

| Путь/место | Что там | Тип | Действие | Примечание |
|---|---|---|---|---|
| `~/Agents/rules/{codex,cursor,devin,mistral}.md` | команды `agent-voice-over send --voice …` и `hey-agent listen … --busy-file ~/.config/jarvis-voice/mic-busy --on-record "jarvis-voice hush"` | doc | заменить | Живые инструкции; `jarvis-voice` как команда — **не существует** в PATH |
| `~/Agents/rules/claude.md:12` | «Голосовой ввод в твоей панели tmux: `hey-claude`» + те же agent-voice-over/jarvis-voice строки | doc | заменить | `hey-claude` — битое имя |
| `~/Projects/global/agents-template/{AGENTS.md,agents/*.md}` | шаблоны тех же ролей: `agent-voice-over` (таблица голосов), `hey-claude` в `agents/claude.md` | doc | заменить | Источник для `~/Agents/rules` — править синхронно |
| `~/Projects/global/hey-agent/README.md:23-31,164-165` | `codex plugin marketplace add john-smith-ceo/hey-claudex`, `/hey-codex`, `hey-claudex stop`, пример с `jarvis-voice` | doc | заменить | После переименования plugin/marketplace |
| `~/Projects/global/hey-agent-dev/README.md` | те же строки (дубль) | doc | заменить | Синхронно |
| `~/Projects/global/hey-agent{,-dev}/REBUILD-PLAN-HEY-AGENT.md:46,125` | план: «Remove hey-claude/hey-codex/hey-claudex aliases», «belongs to agent-voice-over» | doc | оставить/актуализировать | Исторический план v1; §125 отменён решением PO 28.09 |
| `~/Projects/global/hey-agent-dev/SPEC-HEY-AGENT.md` | спека v2: `agent-voice-over` (миграция), `jarvis-voice-drain.py`, окно `hey-agent-voice` | doc | оставить | Рабочий канон; строки-примеры обновятся по ходу |
| `~/Projects/README.md:24,37-39,63-66` | пути `tools/hey-claudex`, `agent-voice-over`, скилл `/hey-claudex` | doc | заменить | Ссылается на несуществующие `~/Projects/tools/…` и `~/Projects/hey-claudex` |
| `~/Projects/PROJECTS-REGISTRY.md:12,16,28,51` | строки `agent-voice-over`, `hey-claudex` (канон-дубль в frozen) | doc | заменить | После cutover переписать строки реестра |
| `~/Projects/AGENTS-CONSOLIDATED.md` | 17 упоминаний | doc | оставить | Архивный снимок правил — не править |
| `~/Tools/linux/README.md:34-35` | строки каталога `hey-claudex/`, `agent-voice-over/` | doc | заменить | Index-файл справочника |
| `~/Tools/linux/hey-claudex/README.md` + `openai-api-key` (файл ключа) | целая страница про hey-claudex; путь `~/Projects/hey-claudex` устарел | doc+secret-file | заменить/архив | Секрет-файл остаётся как источник ключа (или мигрирует по новой схеме) |
| `~/Tools/linux/agent-voice-over/README.md` | страница a-v-o; путь `~/Projects/agent-voice-over` устарел | doc | заменить/архив | Репо переехал в `global/` |
| `~/Tools/linux/openai/README.md:3,11-13` | `tools/hey-claudex/openai-api-key`, EnvironmentFile a-v-o | doc | заменить | Пути stale |
| `~/Tools/linux/hey-agent/README.md:10-11` | пример `--busy-file ~/.config/jarvis-voice/mic-busy` / `jarvis-voice hush` | doc | заменить | После решения PO про новое имя busy-файла |
| `~/Projects/global/mission-control/SUMMARY.md:94` | упоминание хуков `jarvis-voice-*` в старом `~/.claude/settings.json` | doc | оставить | Историческая запись; в текущем `settings.json` хуков нет (проверено) |
| `~/Projects/frozen/archii-imhotect/AGENTS.md:189`, `frozen/jarvis-ear/README.md`, `frozen/pomo/{settings.go,README.md,SUMMARY.md,notify.go}`, `frozen/hey-claudex/*`, `frozen/archive/jarvis-voiceover/*` | десятки упоминаний в frozen | doc/code | архив | Не трогаем — зона замороженных проектов |
| `~/.codex/memories/{MEMORY.md,memory_summary.md,raw_memories.md}`, `rollout_summaries/*` | упоминания hey-codex/claudex/agent-voice-over/codex-voice | memory | оставить | История сессий Codex |
| `~/.codex/memories/rollout_summaries/2026-09-01T12-25-04-Gd4Z-…cleanup.md` и др. | файлы с именами в названиях | memory | оставить | История |

## 6. Код и прочие артефакты

| Путь/место | Что там | Тип | Действие | Примечание |
|---|---|---|---|---|
| `~/Projects/global/hey-agent/cmd/hey-agent/main.go:156,159` | help-текст с `~/.config/jarvis-voice/mic-busy` и `jarvis-voice hush` | code | заменить | Дубль в `hey-agent-dev/cmd/hey-agent/main.go:156,159` |
| `~/Projects/global/hey-agent{,-dev}/cmd/hey-agent/main.go:34` | `installNames = ["hey-agent"]` | code | оставить | Уже единое имя — ничего лишнего не ставится |
| `~/Projects/global/hey-agent{,-dev}/cmd/hey-agent/main.go:29` | `voiceWindow = "hey-agent-voice"` — имя tmux-окна слушателя | code | оставить/решение PO | Имя новое, но SPEC v2: окно уходит под systemd (`audit-spec-vs-impl.md:20`) |
| tmux-опции `@hey-agent-status-*`/`@hey-agent-length-*` (`internal/tmux/status.go:19-22` в `hey-agent` и `hey-agent-dev`) | status-line маркеры | code | оставить | Новое имя |
| `~/Projects/frozen/hey-claudex/cmd/hey-claudex/main.go:26,29,32` | `keychainService = "hey-claudex.openai-api-key"`, `voiceWindow = "hey-claudex-voice"`, `installNames = [hey-claudex, hey-claude, hey-codex]` | code | архив | Источник троицы имён |
| `~/Projects/global/agent-voice-over/go.mod` | module `github.com/john-smith-ceo/agent-voice-over` | code | архив с репо | Модульное имя |
| `~/Projects/frozen/hey-claudex/packaging/homebrew/hey-claudex.rb.tmpl` | формула | file | архив | — |
| `~/Projects/global/agent-voice-over/{install.sh,uninstall.sh}` | инсталлятор: пишет `~/.local/bin/agent-voice-over`, symlinks lexicons/profiles, unit-файлы, `~/.config/agent-voice-over/.secret` | script | архив с репо | `uninstall.sh` — референс того, что снимать при cutover |
| `~/Projects/global/agent-voice-over/docs/cleanup-plan.md` | выполненный план чистки (05.09): jarvis-* → agent-voice-over | doc | архив | Полезный прецедент для этого cutover |
| `~/Projects/global/hey-agent-dev/docs/voiceover-features.md` | инвентаризация a-v-o (25 фич, артефакты, битые ссылки) | doc | оставить | Основной источник этого списка |
| `~/Projects/global/hey-agent-dev/scripts/stand-down.sh:64` | комментарий про окно `hey-agent-voice` | script | оставить | Новое имя |
| Запущенные процессы | `agent-voice-over daemon` (pid 1248); слушатель `hey-agent run --attached` в tmux-окне `hey-agent-voice` (сессия `5`, панель `%7`) | runtime | остановить при cutover | См. snapshot-2026-09-28.md |
| GitHub remote | `github.com/john-smith-ceo/hey-claudex` — marketplace-source в codex config | remote | переименовать/создать | Репо hey-claudex на GitHub — источник marketplace; после переименования обновить `config.toml` |

## Не нашлось

Искал, но не обнаружено (для полноты картины):

- `hey-voice` — **ноль** совпадений во всём `~/Projects` (grep по всем файлам) и
  в `~/.local/bin`, `~/.config`; имени нигде нет.
- Живые бинари `hey-claude`, `hey-codex`, `hey-claudex`, `jarvis-voice`,
  `codex-voice` в PATH — `command -v` даёт MISSING по всем; остались только
  битые symlinks и бэкапы в `~/.local/bin`.
- `~/.config/systemd/user/` — других unit-файлов со старыми именами
  (`hey-claude*`, `jarvis-voice*`, `hey-voice*`, `codex-voice*`) нет; есть только
  `agent-voice-over.service` и битый `-tray`.
- `~/.config/autostart/` (включённые) — голосовых/jarvis записей нет;
  `jarvis-ear.desktop` лежит только в `autostart.disabled`.
- `~/.claude/settings.json` — хуков `jarvis-voice-*` уже нет (убраны ранее;
  упоминание осталось только в `mission-control/SUMMARY.md`).
- `~/.codex/rules/default.rules` — белый список команд не содержит имён
  hey-*/voice/jarvis.
- `~/.config/devin/{config.json,mcp_config.json}` — упоминаний нет (только
  `skills/hey-claudex/SKILL.md`).
- `~/Projects/tools/` — каталога не существует: пути `tools/openai/.env` и
  `tools/hey-claudex/openai-api-key` (в юните, README, `~/.config/hey-claudex`)
  мертвы.
- `~/Projects/the-jarvis` — не существует (stale trust-entry в codex config).
- shell rc (`~/.bashrc`, `~/.zshrc`, `~/.profile`, `~/.bash_aliases`,
  `fish/config.fish`) — упоминаний нет; `crontab -l` — пусто;
  `/etc/systemd/system/` — совпадений нет.
- `~/.cursor`, `~/.codeium`, `~/.windsurf` — файлов со старыми именами нет
  (windsurf отсутствует как каталог).
- `~/Projects/kindi`, `~/Projects/local`, `~/Projects/global/{session-log,
  deep-space-explorer, brandts-master, maxmodule, sir-john-shell, archimax}` —
  по общему grep `~/Projects` файлов с искомыми именами не найдено.
