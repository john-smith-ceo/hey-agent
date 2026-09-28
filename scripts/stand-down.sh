#!/usr/bin/env bash
# stand-down.sh — гасит тестовый стенд, поднятый stand-up.sh.
#
# Порядок важен: сначала убиваем процессы по pid-файлам и tmux-сервер
# (по сокету `heydev`, дефолтный сервер пользователя не трогаем), потом
# удаляем каталог состояния. Скрипт идемпотентен: на уже погашенном стенде
# просто молча проходит до конца — все проверки «а есть ли ещё кто живой»
# встроены.

set -uo pipefail   # без -e: гасим всё, что можно, и не падаем на первой же
                  # «уже мёртвой» части стенда

STAND_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/hey-agent-stand"
TMUX_SOCKET="heydev"

say() { printf '%s\n' "$*"; }

say "== hey-agent test stand down =="

# stop_pid <pid-файл> <имя>:
#   мягкий TERM → до 3 секунд ожидания → если жив, добить KILL.
#   Файла/процесса нет — сообщаем и идём дальше (идемпотентность).
stop_pid() {
    local file="$1" name="$2" pid i
    if [ ! -f "$file" ]; then
        say "$name: pid-файла нет — пропускаю"
        return 0
    fi
    pid="$(cat "$file" 2>/dev/null)"
    if [ -z "$pid" ] || ! kill -0 "$pid" 2>/dev/null; then
        say "$name: процесс уже мёртв (pid ${pid:-?})"
        rm -f "$file"
        return 0
    fi
    kill "$pid" 2>/dev/null
    for i in $(seq 1 30); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.1
    done
    if kill -0 "$pid" 2>/dev/null; then
        kill -9 "$pid" 2>/dev/null
        say "$name: не умер по TERM, добит KILL (pid $pid)"
    else
        say "$name: остановлен (pid $pid)"
    fi
    rm -f "$file"
}

# --- 1. Процессы стенда по pid-файлам ---------------------------------------

stop_pid "$STAND_DIR/mock.pid" "mock-provider"

# Чужой Xvfb на :99 (xvfb.external) — не трогаем: он не наш.
if [ -f "$STAND_DIR/xvfb.external" ]; then
    say "xvfb: дисплей занят чужим сервером — оставляю как есть"
    rm -f "$STAND_DIR/xvfb.external"
else
    stop_pid "$STAND_DIR/xvfb.pid" "xvfb"
fi

# --- 2. tmux-сервер heydev целиком -------------------------------------------
#
# kill-server снимает сервер со всеми сессиями/окнами стенда (a, b, ctl,
# hey-agent-voice — если слушатель был запущен). Сокет указываем явно через
# -L: сессии и сервер пользователя остаются нетронутыми.
if tmux -L "$TMUX_SOCKET" ls >/dev/null 2>&1; then
    tmux -L "$TMUX_SOCKET" kill-server
    say "tmux: сервер '$TMUX_SOCKET' остановлен"
else
    say "tmux: сервер '$TMUX_SOCKET' уже не работает — пропускаю"
fi

# Иногда kill-server оставляет пустой файл сокета — сервера уже нет, но
# мусор в /tmp/tmux-$UID/ остаётся. Удаляем только если сервер точно мёртв
# и только файл нашего сокета — дефолтный сокет не трогаем никогда.
SOCKET_FILE="/tmp/tmux-$(id -u)/$TMUX_SOCKET"
if [ -S "$SOCKET_FILE" ] && ! tmux -L "$TMUX_SOCKET" ls >/dev/null 2>&1; then
    rm -f "$SOCKET_FILE"
    say "tmux: убран оставшийся файл сокета $SOCKET_FILE"
fi

# --- 3. Каталог состояния ----------------------------------------------------
# Внутри только наши pid-файлы, маркеры и логи — удалять безопасно.
if [ -d "$STAND_DIR" ]; then
    rm -rf "$STAND_DIR"
    say "каталог $STAND_DIR удалён"
fi

say "стенд погашен."
