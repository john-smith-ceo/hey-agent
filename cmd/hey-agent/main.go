package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/john-smith-ceo/hey-agent/internal/bridge"
	"github.com/john-smith-ceo/hey-agent/internal/hotkey"
	"github.com/john-smith-ceo/hey-agent/internal/record"
	"github.com/john-smith-ceo/hey-agent/internal/tmux"
	"github.com/john-smith-ceo/hey-agent/internal/transcribe"
)

// voiceWindow is the hidden tmux window the listener runs in.
const voiceWindow = "hey-agent-voice"

const version = "0.1.3"

// installNames are the names the tool is reachable under.
var installNames = []string{"hey-agent"}

func expectedApp() string {
	return ""
}

// isOwnName reports whether the command is hey-agent.
func isOwnName(command string) bool {
	for _, name := range installNames {
		if command == name {
			return true
		}
	}
	return false
}

// paneCommand reports the program currently running in a pane.
func paneCommand(pane string) string {
	output, err := exec.Command("tmux", "display-message", "-p", "-t", pane, "#{pane_current_command}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func main() {
	if len(os.Args) < 2 {
		os.Exit(listen(nil))
	}
	if os.Args[1] == "version" || os.Args[1] == "--version" {
		fmt.Println("hey-agent", version)
		return
	}

	// A bare flag means listen: `hey-agent --silence 2s` should not require
	// spelling out the only command the tool really has.
	if strings.HasPrefix(os.Args[1], "-") && os.Args[1] != "-h" && os.Args[1] != "--help" {
		os.Exit(listen(os.Args[1:]))
	}

	switch os.Args[1] {
	case "listen":
		os.Exit(listen(os.Args[2:]))
	case "config":
		os.Exit(configure(os.Args[2:]))
	case "keys":
		os.Exit(listKeys(os.Stdout))
	case "doctor":
		os.Exit(doctor(os.Args[2:], os.Stdout))
	case "setup-key":
		os.Exit(setupAPIKey(os.Args[2:], os.Stdin, os.Stdout))
	case "install":
		os.Exit(install(os.Stdout))
	case "stop":
		os.Exit(stop(os.Args[2:]))
	case "run":
		os.Exit(run(os.Args[2:]))
	case "help", "--help", "-h":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `hey-agent — голосовой ввод в явно выбранную tmux-панель

Самый простой старт
	  hey-agent listen
	      Включит голосовой ввод в текущей панели tmux.

Как говорить
  1. Нажмите правый Alt (на маке — правый Option) один раз.
  2. Скажите задачу.
  3. Нажмите ещё раз — или просто помолчите (по умолчанию пять секунд,
     меняется флагом --silence).
  4. Текст появится в строке ввода. Проверьте его и нажмите Enter сами.
     С флагом --submit он уходит сразу, без проверки.

Первый запуск
	  hey-agent setup-key
	      Сохранит ключ провайдера в tools/hey-agent/.env с правами 600.
	  hey-agent doctor
      Проверит микрофон, tmux, ffmpeg, горячую клавишу и ключ.

Полезные команды
	  hey-agent listen           Включить голос в текущей панели.
	  hey-agent config           Изменить режим следующей записи.
	  hey-agent version          Показать версию.
	  hey-agent stop             Остановить голосовой ввод.
	  hey-agent keys             Показать клавиши, которые можно назначить.
	  hey-agent listen --key Shift_L
                             Назначить свою клавишу.
	  hey-agent listen --mode push
	                             Говорить, пока клавиша удерживается.
	  hey-agent listen --submit Отправлять сразу: замолчали или отпустили
                             клавишу — текст ушёл.
	  hey-agent doctor --verify-api
                             Проверить доступ к OpenAI без отправки аудио.

Строка tmux
  Состояние видно внизу терминала: mode:tap, rec…, transcribe…, done
  или error. Когда включена отправка без проверки, режим показан
	  как mode:tap auto — чтобы это не оказалось неожиданностью. В вашей сессии hey-agent только дописывает значок справа,
  а прежнее содержимое возвращает при остановке. Ваше оформление
  остаётся вашим.

Отправка без проверки
  По умолчанию Enter за вас никто не нажимает. Флаг --submit это меняет:
  расшифровка уходит сразу, как только запись закончилась — по паузе или
  по отпущенной клавише. Флаг не связан с режимом: удержание с отправкой
  работает так же, как нажатие с отправкой.
  Между вставкой и Enter выдерживается четверть секунды: приложение ещё
  дочитывает вставленный текст, и Enter в том же всплеске событий оно
  проглотит как часть вставки. Медленному приложению паузу можно
  увеличить: --submit-delay 500ms.

Тишина на время записи
  Если ответы читаются вслух, чтец попадает в тот же микрофон и оседает
  в расшифровке. Два флага это лечат:
    --busy-file ~/.config/jarvis-voice/mic-busy
                             Держать метку «микрофон занят», пока идёт
                             запись; читающий её дожидается.
    --on-record "jarvis-voice hush"
                             Оборвать то, что уже звучит: меткой этого
                             не сделать, фраза договорила бы до конца.
	  Оба берутся и из окружения: HEY_AGENT_BUSY_FILE, HEY_AGENT_ON_RECORD.

Безопасность
	  hey-agent ничего не запускает: он работает только в панели, которая
  уже открыта, и говорит только в неё — без поиска активного окна.
  Пустую расшифровку не отправляет никогда.

`)
}

// listKeys prints the closed list of selectable hotkeys. The list is closed on
// purpose: a key that takes part in typing needs different handling, and an
// arbitrary key would silently get the wrong one.
func listKeys(w io.Writer) int {
	fmt.Fprintln(w, "Клавиши, которые можно назначить (--key):")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Свободные — в наборе текста не участвуют, работают оба режима:")
	for _, key := range hotkey.Names() {
		if key.Category != hotkey.Free {
			continue
		}
		fmt.Fprintln(w, "    "+describe(key))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Рабочие — нажимаются при наборе, поэтому только --mode tap")
	fmt.Fprintln(w, "  и только одиночное нажатие без других клавиш:")
	for _, key := range hotkey.Names() {
		if key.Category != hotkey.Typing {
			continue
		}
		fmt.Fprintln(w, "    "+describe(key))
	}
	fmt.Fprintf(w, "\nПо умолчанию: %s\n", hotkey.Default)
	return 0
}

func describe(key hotkey.Key) string {
	line := fmt.Sprintf("%-12s", key.Name)
	if len(key.Aliases) > 0 {
		line += " (" + strings.Join(key.Aliases, ", ") + ")"
	}
	if !key.AvailableOnDarwin() {
		line += " — на клавиатурах Apple такой клавиши нет"
	}
	return strings.TrimRight(line, " ")
}

func doctor(args []string, w io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	verifyAPI := fs.Bool("verify-api", false, "verify API access without sending audio")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	failures := 0
	required := []string{"ffmpeg", "tmux"}
	if runtime.GOOS == "darwin" {
		required = append(required, "security")
	}
	for _, binary := range required {
		if path, err := exec.LookPath(binary); err == nil {
			fmt.Fprintf(w, "ok   %-10s %s\n", binary, path)
		} else {
			fmt.Fprintf(w, "fail %-10s not found\n", binary)
			failures++
		}
	}
	if hotKey, err := hotkey.Lookup(hotkey.Default); err != nil {
		fmt.Fprintln(w, "fail hotkey table:", err)
		failures++
	} else if _, err := hotkey.New(hotKey); err != nil {
		fmt.Fprintln(w, "fail hotkey:", err)
		failures++
	} else {
		fmt.Fprintf(w, "ok   hotkey     %s available\n", hotKey.Name)
	}
	key := os.Getenv("HEY_AGENT_API_KEY")
	if key == "" {
		key, _ = loadDotenvKey(agentEnvFile())
	}
	if key != "" {
		fmt.Fprintln(w, "ok   provider API key available")
		if *verifyAPI {
			provider := transcribe.NewProvider(transcribe.Config{
				APIKey:  key,
				BaseURL: os.Getenv("HEY_AGENT_BASE_URL"),
				Model:   os.Getenv("HEY_AGENT_MODEL"),
			})
			if err := provider.Verify(context.Background()); err != nil {
				fmt.Fprintln(w, "fail provider API access:", err)
				failures++
			} else {
				fmt.Fprintln(w, "ok   provider API access")
			}
		}
	} else {
		fmt.Fprintln(w, "fail API key missing (run: hey-agent setup-key)")
		failures++
	}
	if runtime.GOOS == "darwin" {
		fmt.Fprintln(w, "note grant Microphone and Accessibility permission to the launcher application before run")
	} else {
		fmt.Fprintln(w, "note hey-agent needs an X11 session; Wayland is not supported yet")
	}
	if failures > 0 {
		return 1
	}
	return 0
}

func setupAPIKey(args []string, in io.Reader, out io.Writer) int {
	fs := flag.NewFlagSet("setup-key", flag.ContinueOnError)
	envFile := fs.String("env-file", "", "read OPENAI_API_KEY from a dotenv file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var key string
	if *envFile != "" {
		loaded, err := loadDotenvKey(*envFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "load API key:", err)
			return 1
		}
		key = loaded
	} else {
		fmt.Fprint(out, "Provider API key: ")
		if file, ok := in.(*os.File); ok && file == os.Stdin {
			if err := exec.Command("stty", "-echo").Run(); err == nil {
				defer func() {
					_ = exec.Command("stty", "echo").Run()
					fmt.Fprintln(out)
				}()
			}
		}
		entered, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintln(os.Stderr, "read API key:", err)
			return 1
		}
		key = strings.TrimSpace(entered)
	}
	if err := saveDotenvKey(agentEnvFile(), key); err != nil {
		fmt.Fprintln(os.Stderr, "save API key:", err)
		return 1
	}
	fmt.Fprintln(out, "saved:", agentEnvFile())
	return 0
}

func agentEnvFile() string {
	if path := strings.TrimSpace(os.Getenv("HEY_AGENT_ENV_FILE")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "tools/hey-agent/.env"
	}
	return filepath.Join(home, "Projects", "tools", "hey-agent", ".env")
}

func saveDotenvKey(path, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("API key is empty")
	}
	if strings.ContainsAny(key, "\r\n") {
		return errors.New("API key must be a single line")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	contents := "HEY_AGENT_API_KEY=" + strings.TrimSpace(key) + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func loadDotenvKey(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		name, value, found := strings.Cut(line, "=")
		if !found || !dotenvKeyNames[strings.TrimSpace(name)] {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			} else {
				value = value[1 : len(value)-1]
			}
		}
		if value == "" {
			return "", fmt.Errorf("%s is empty", strings.TrimSpace(name))
		}
		return value, nil
	}
	return "", errors.New("neither OPENAI_API_KEY nor OPEN_AI_API_KEY found")
}

// Both spellings occur in the wild, and a key file is not worth renaming just
// to satisfy a parser.
var dotenvKeyNames = map[string]bool{
	"HEY_AGENT_API_KEY": true,
	"OPENAI_API_KEY":    true,
	"OPEN_AI_API_KEY":   true,
}

// install links the built binary under all three names. The aliases are not
// decoration: the name states which assistant the pane is expected to run.
func install(out io.Writer) int {
	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Fprintln(os.Stderr, "tmux is required; install it first")
		return 1
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "locate executable:", err)
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "locate home directory:", err)
		return 1
	}
	targetDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "create install directory:", err)
		return 1
	}
	for _, name := range installNames {
		target := filepath.Join(targetDir, name)
		if info, err := os.Lstat(target); err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				fmt.Fprintf(os.Stderr, "refusing to overwrite non-symlink %s\n", target)
				return 1
			}
			current, err := filepath.EvalSymlinks(target)
			if err != nil || current != executable {
				fmt.Fprintf(os.Stderr, "refusing to replace existing link %s\n", target)
				return 1
			}
			fmt.Fprintf(out, "already installed: %s\n", target)
			continue
		}
		if err := os.Symlink(executable, target); err != nil {
			fmt.Fprintln(os.Stderr, "install symlink:", err)
			return 1
		}
		fmt.Fprintf(out, "installed: %s -> %s\n", target, executable)
	}
	return 0
}

func run(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	mode := fs.String("mode", "tap", "recording mode: tap or push")
	silence := fs.Duration("silence", 5*time.Second, "tap-mode silence timeout")
	device := fs.String("device", record.DefaultDevice(), "audio input device")
	busyFile := fs.String("busy-file", os.Getenv("HEY_AGENT_BUSY_FILE"), "file to touch while recording, so voice-overs stay quiet")
	onRecord := fs.String("on-record", os.Getenv("HEY_AGENT_ON_RECORD"), "shell command run when recording starts")
	submit := fs.Bool("submit", false, "press Enter after the transcription lands")
	submitDelay := fs.Duration("submit-delay", tmux.DefaultSubmitDelay, "pause between the paste and Enter")
	keyName := fs.String("key", hotkey.Default, "hotkey; run: hey-agent keys")
	tmuxTarget := fs.String("tmux-target", "hey-agent:0.0", "tmux pane receiving transcriptions")
	tmuxSession := fs.String("tmux-session", "hey-agent", "tmux session owning the hey-agent status line")
	runtimeConfig := fs.String("runtime-config", "", "runtime settings file")

	attached := fs.Bool("attached", false, "the session belongs to the user: borrow the status line instead of taking it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *mode != "tap" && *mode != "push" {
		fmt.Fprintln(os.Stderr, "--mode must be tap or push")
		return 2
	}
	hotKey, err := hotkey.Lookup(*keyName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := hotkey.Validate(hotKey); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	key, err := openAIKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// The status line shows what is actually running in the target pane rather
	// than what a flag claims, so it cannot drift from reality.
	shown := *mode
	if *submit {
		shown += " auto"
	}
	status, err := tmux.NewStatus(*tmuxSession, paneCommand(*tmuxTarget), shown, *attached)
	if err != nil {
		fmt.Fprintln(os.Stderr, "initialize tmux status:", err)
		return 1
	}
	if err := status.Configure(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "configure tmux status:", err)
		return 1
	}
	app, err := bridge.New(bridge.Config{Mode: bridge.Mode(*mode), Silence: *silence, Device: *device, APIKey: key, BaseURL: os.Getenv("HEY_AGENT_BASE_URL"), Model: os.Getenv("HEY_AGENT_MODEL"), Log: os.Stderr, RuntimeConfig: *runtimeConfig, State: func(state string) {
		if err := status.Set(context.Background(), state); err != nil {
			fmt.Fprintln(os.Stderr, "update tmux status:", err)
		}
	}, TmuxTarget: *tmuxTarget, Key: hotKey, BusyFile: *busyFile, OnRecord: *onRecord, Submit: *submit, SubmitDelay: *submitDelay})
	if err != nil {
		fmt.Fprintln(os.Stderr, "initialize:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "hey-agent ready: %s (%s mode), target %s; Ctrl+C stops the listener\n", hotKey.Name, *mode, *tmuxTarget)
	runErr := app.Run(context.Background())
	if err := status.Restore(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "restore tmux status:", err)
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		fmt.Fprintln(os.Stderr, "run:", runErr)
		return 1
	}
	if *runtimeConfig != "" {
		_ = os.Remove(*runtimeConfig)
	}
	return 0
}

type runtimeSettings struct {
	Mode    string `json:"mode"`
	Silence string `json:"silence"`
	Submit  bool   `json:"submit"`
}

func configure(args []string) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	mode := fs.String("mode", "", "next recording mode: tap or push")
	silence := fs.String("silence", "", "pause ending tap-mode recording, for example 2s")
	submit := fs.Bool("submit", false, "submit the next transcription automatically")
	noSubmit := fs.Bool("no-submit", false, "do not submit the next transcription automatically")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *mode != "" && *mode != "tap" && *mode != "push" {
		fmt.Fprintln(os.Stderr, "--mode must be tap or push")
		return 2
	}
	if *submit && *noSubmit {
		fmt.Fprintln(os.Stderr, "--submit and --no-submit cannot be used together")
		return 2
	}
	if *silence != "" {
		d, err := time.ParseDuration(*silence)
		if err != nil || d <= 0 {
			fmt.Fprintln(os.Stderr, "--silence must be a positive duration, for example 2s")
			return 2
		}
	}
	if !insideTmux() {
		fmt.Fprintln(os.Stderr, "hey-agent config must run inside the tmux session being listened to")
		return 2
	}
	session, err := currentSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	path := runtimeConfigPath(session)
	settings, err := loadRuntimeSettings(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listener is not running in %q; start it with: hey-agent listen\n", session)
		return 1
	}
	if flagWasSet(fs, "mode") {
		settings.Mode = *mode
	}
	if flagWasSet(fs, "silence") {
		settings.Silence = *silence
	}
	if *submit {
		settings.Submit = true
	}
	if *noSubmit {
		settings.Submit = false
	}
	if err := saveRuntimeSettings(path, settings); err != nil {
		fmt.Fprintln(os.Stderr, "save listener settings:", err)
		return 1
	}
	fmt.Printf("Следующая запись: mode=%s silence=%s submit=%t\n", settings.Mode, settings.Silence, settings.Submit)
	return 0
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	wasSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			wasSet = true
		}
	})
	return wasSet
}

func runtimeConfigPath(session string) string {
	sum := sha256.Sum256([]byte(session))
	return filepath.Join(os.TempDir(), "hey-agent-"+hex.EncodeToString(sum[:8])+".json")
}

func loadRuntimeSettings(path string) (runtimeSettings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return runtimeSettings{}, err
	}
	var settings runtimeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return runtimeSettings{}, err
	}
	return settings, nil
}

func saveRuntimeSettings(path string, settings runtimeSettings) error {
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hey-agent-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// listen attaches to the session the user is already in: the transcription goes
// to the pane it was called from, nothing is created, nothing is taken over.
func listen(args []string) int {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	mode := fs.String("mode", "tap", "voice mode: tap or push")
	keyName := fs.String("key", hotkey.Default, "hotkey; run: hey-agent keys")
	target := fs.String("target", "", "tmux pane receiving transcriptions (default: the pane you run this from)")
	silence := fs.Duration("silence", 5*time.Second, "how long a pause ends the recording in tap mode")
	device := fs.String("device", record.DefaultDevice(), "audio input device")
	busyFile := fs.String("busy-file", os.Getenv("HEY_AGENT_BUSY_FILE"), "file to touch while recording, so voice-overs stay quiet")
	onRecord := fs.String("on-record", os.Getenv("HEY_AGENT_ON_RECORD"), "shell command run when recording starts")
	submit := fs.Bool("submit", false, "press Enter after the transcription lands")
	submitDelay := fs.Duration("submit-delay", tmux.DefaultSubmitDelay, "pause between the paste and Enter")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !insideTmux() {
		fmt.Fprintln(os.Stderr, "hey-agent speaks into a pane you are already working in, so it needs tmux.")
		fmt.Fprintln(os.Stderr, "open tmux, start your assistant there, and call this from that pane.")
		return 2
	}
	if *mode != "tap" && *mode != "push" {
		fmt.Fprintln(os.Stderr, "--mode must be tap or push")
		return 2
	}
	hotKey, err := hotkey.Lookup(*keyName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := hotkey.Validate(hotKey); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if *mode == "push" && !hotKey.SupportsPush() {
		fmt.Fprintf(os.Stderr, "%s is pressed while typing, so hold-to-talk is impossible for it; use --mode tap\n", hotKey.Name)
		return 2
	}
	pane := *target
	if pane == "" {
		pane = os.Getenv("TMUX_PANE")
	}
	if pane == "" {
		fmt.Fprintln(os.Stderr, "cannot tell which pane to speak into; pass --target")
		return 1
	}
	session, err := currentSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if tmuxWindowExists(session, voiceWindow) {
		fmt.Fprintf(os.Stderr, "hey-agent is already listening in %q; stop it with: hey-agent stop\n", session)
		return 1
	}
	settingsPath := runtimeConfigPath(session)
	if err := saveRuntimeSettings(settingsPath, runtimeSettings{Mode: *mode, Silence: silence.String(), Submit: *submit}); err != nil {
		fmt.Fprintln(os.Stderr, "save listener settings:", err)
		return 1
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "locate hey-agent executable:", err)
		return 1
	}
	command := []string{"new-window", "-d", "-t", tmuxSessionTarget(session), "-n", voiceWindow, "-c", mustGetwd(), executable,
		"run", "--attached", "--mode", *mode, "--key", hotKey.Name, "--tmux-target", pane, "--tmux-session", session,
		"--silence", silence.String(), "--device", *device,
		"--busy-file", *busyFile, "--on-record", *onRecord,
		"--submit=" + strconv.FormatBool(*submit), "--submit-delay", submitDelay.String(), "--runtime-config", settingsPath}
	if output, err := exec.Command("tmux", command...).CombinedOutput(); err != nil {
		_ = os.Remove(settingsPath)
		fmt.Fprintln(os.Stderr, "start voice listener:", strings.TrimSpace(string(output)))
		return 1
	}
	if *submit {
		fmt.Printf("Слушаю: %s (%s, пауза %s). Речь уйдёт в панель %s сразу, без проверки.\n", hotKey.Name, *mode, *silence, pane)
	} else {
		fmt.Printf("Слушаю: %s (%s, пауза %s). Речь придёт в панель %s — проверьте текст и нажмите Enter сами.\n", hotKey.Name, *mode, *silence, pane)
	}
	fmt.Println("Остановить: hey-agent stop")
	return 0
}

// stop removes the listener and puts the borrowed status line back. The user's
// session is never killed: hey-agent did not create it and has no business
// taking it down.
func stop(args []string) int {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !insideTmux() {
		fmt.Fprintln(os.Stderr, "run this from the tmux session where hey-agent is listening")
		return 2
	}
	current, err := currentSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// The window may already be gone — a crash, or somebody closing it by hand.
	// The borrowed status line still has to be given back, so cleaning up runs
	// either way.
	if tmuxWindowExists(current, voiceWindow) {
		if output, err := exec.Command("tmux", "kill-window", "-t", tmuxSessionTarget(current)+voiceWindow).CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, "stop listener:", strings.TrimSpace(string(output)))
			return 1
		}
		_ = os.Remove(runtimeConfigPath(current))
		restored, err := tmux.Restore(context.Background(), current)
		if err != nil {
			fmt.Fprintln(os.Stderr, "restore tmux status:", err)
		}
		if restored {
			fmt.Printf("Слушатель остановлен, строка состояния сессии %q возвращена.\n", current)
		} else {
			fmt.Println("Слушатель остановлен.")
		}
		return 0
	}
	restored, err := tmux.Restore(context.Background(), current)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore tmux status:", err)
		return 1
	}
	if restored {
		_ = os.Remove(runtimeConfigPath(current))
		fmt.Printf("Слушателя уже не было, но строка состояния сессии %q осталась занятой — вернул.\n", current)
		return 0
	}
	fmt.Fprintf(os.Stderr, "nothing to stop: hey-agent is not listening in %q\n", current)
	return 1
}

func insideTmux() bool { return strings.TrimSpace(os.Getenv("TMUX")) != "" }

func currentSession() (string, error) {
	output, err := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
	if err != nil {
		return "", fmt.Errorf("cannot tell which tmux session this is: %w", err)
	}
	name := strings.TrimSpace(string(output))
	if name == "" {
		return "", errors.New("cannot tell which tmux session this is")
	}
	return name, nil
}

func openAIKey() (string, error) {
	if key := strings.TrimSpace(os.Getenv("HEY_AGENT_API_KEY")); key != "" {
		return key, nil
	}
	key, err := loadDotenvKey(agentEnvFile())
	if err == nil && strings.TrimSpace(key) != "" {
		return key, nil
	}
	return "", fmt.Errorf("API key missing; run hey-agent setup-key (env file: %s)", agentEnvFile())
}

func tmuxWindowExists(session, name string) bool {
	output, err := exec.Command("tmux", "list-windows", "-t", tmuxSessionTarget(session), "-F", "#{window_name}").Output()
	if err != nil {
		return false
	}
	for _, window := range strings.Fields(string(output)) {
		if window == name {
			return true
		}
	}
	return false
}

// tmuxSessionTarget makes a session name unambiguous. Without the trailing
// colon, a numeric session name such as "1" is parsed by tmux as window index
// 1. That makes `new-window -t 1` fail with "index 1 in use" instead of
// creating a window in session "1".
func tmuxSessionTarget(session string) string { return session + ":" }

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
