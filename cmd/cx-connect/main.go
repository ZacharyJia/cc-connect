package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ZacharyJia/cx-connect/config"
	"github.com/ZacharyJia/cx-connect/core"
	"github.com/ZacharyJia/cx-connect/daemon"

	_ "github.com/ZacharyJia/cx-connect/agent/claudecode"
	_ "github.com/ZacharyJia/cx-connect/agent/codex"
	_ "github.com/ZacharyJia/cx-connect/agent/cursor"
	_ "github.com/ZacharyJia/cx-connect/agent/gemini"

	_ "github.com/ZacharyJia/cx-connect/platform/dingtalk"
	_ "github.com/ZacharyJia/cx-connect/platform/discord"
	_ "github.com/ZacharyJia/cx-connect/platform/feishu"
	_ "github.com/ZacharyJia/cx-connect/platform/line"
	_ "github.com/ZacharyJia/cx-connect/platform/qq"
	_ "github.com/ZacharyJia/cx-connect/platform/slack"
	_ "github.com/ZacharyJia/cx-connect/platform/telegram"
	_ "github.com/ZacharyJia/cx-connect/platform/wecom"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	// Handle subcommands before flag parsing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "update":
			runUpdate()
			return
		case "check-update":
			checkUpdate()
			return
		case "provider":
			runProviderCommand(os.Args[2:])
			return
		case "send":
			runSend(os.Args[2:])
			return
		case "cron":
			runCron(os.Args[2:])
			return
		case "daemon":
			runDaemon(os.Args[2:])
			return
		}
	}

	// When started as a daemon (CC_LOG_FILE set), redirect logs to a rotating file.
	var logWriter io.Writer
	var logCloser io.Closer
	if logFile := os.Getenv("CC_LOG_FILE"); logFile != "" {
		maxSize := int64(daemon.DefaultLogMaxSize)
		if v := os.Getenv("CC_LOG_MAX_SIZE"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				maxSize = n
			}
		}
		w, err := daemon.NewRotatingWriter(logFile, maxSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open log file %s: %v\n", logFile, err)
			os.Exit(1)
		}
		logWriter = w
		logCloser = w
	}
	if logCloser != nil {
		defer logCloser.Close()
	}

	configFlag := flag.String("config", "", "path to config file (default: ./config.toml or ~/.cx-connect/config.toml)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("cx-connect %s\ncommit:  %s\nbuilt:   %s\n", version, commit, buildTime)
		return
	}

	core.VersionInfo = fmt.Sprintf("cx-connect %s\ncommit: %s\nbuilt: %s", version, commit, buildTime)

	configPath := resolveConfigPath(*configFlag)

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := bootstrapConfig(configPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created default config at %s\n", configPath)
		fmt.Println("Please edit this file to add your agent and platform credentials, then run cx-connect again.")
		os.Exit(0)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config (%s): %v\n", configPath, err)
		os.Exit(1)
	}

	config.ConfigPath = configPath
	slog.Info("config loaded", "path", configPath)

	setupLogger(cfg.Log.Level, logWriter)

	// Create engine from config (single agent + platforms)
	engine := createEngine(cfg)

	// Start cron scheduler
	cronStore, err := core.NewCronStore(cfg.DataDir)
	if err != nil {
		slog.Warn("cron store unavailable", "error", err)
	}
	var cronSched *core.CronScheduler
	if cronStore != nil {
		cronSched = core.NewCronScheduler(cronStore)
		cronSched.RegisterEngine("default", engine)
		engine.SetCronScheduler(cronSched)
	}

	if err := engine.Start(); err != nil {
		slog.Error("failed to start engine", "error", err)
		os.Exit(1)
	}

	if cronSched != nil {
		if err := cronSched.Start(); err != nil {
			slog.Error("cron scheduler start failed", "error", err)
		}
	}

	// Start internal API server for CLI send
	apiSrv, err := core.NewAPIServer(cfg.DataDir)
	if err != nil {
		slog.Warn("api server unavailable", "error", err)
	} else {
		apiSrv.RegisterEngine("default", engine)
		if cronSched != nil {
			apiSrv.SetCronScheduler(cronSched)
		}
		apiSrv.Start()
	}

	slog.Info("cx-connect is running")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("shutting down...")
	if cronSched != nil {
		cronSched.Stop()
	}
	if apiSrv != nil {
		apiSrv.Stop()
	}
	if err := engine.Stop(); err != nil {
		slog.Error("shutdown error", "error", err)
	}
	slog.Info("bye")
}

// sessionStorePath builds a unique filename from project name + work_dir.
// It checks local .cx-connect/ first; if no local file exists, it falls back to dataDir/sessions/.
func sessionStorePath(dataDir, name, workDir string) string {
	var filename string
	if workDir == "" {
		filename = name + ".json"
	} else {
		abs, err := filepath.Abs(workDir)
		if err != nil {
			abs = workDir
		}
		h := sha256.Sum256([]byte(abs))
		short := hex.EncodeToString(h[:4])
		filename = fmt.Sprintf("%s_%s.json", name, short)
	}

	// Check local path: .cx-connect/<name>.json or .cx-connect/<name>.sessions.json
	for _, candidate := range []string{
		filepath.Join(".cx-connect", filename),
		filepath.Join(".cx-connect", strings.TrimSuffix(filename, ".json")+".sessions.json"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			slog.Info("session: using local file", "path", candidate)
			return candidate
		}
	}

	return filepath.Join(dataDir, "sessions", filename)
}

// resolveConfigPath determines which config file to use.
// Priority: explicit flag → ./config.toml → ~/.cx-connect/config.toml
func resolveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if _, err := os.Stat("config.toml"); err == nil {
		return "config.toml"
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cx-connect", "config.toml")
	}
	return "config.toml"
}

func bootstrapConfig(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	const tmpl = `# cx-connect configuration
# Docs: https://github.com/ZacharyJia/cx-connect

[log]
level = "info"

[agent]
type = "claudecode"   # "claudecode", "codex", "cursor", or "gemini"

[agent.options]
work_dir = "/path/to/your/project"
mode = "default"
# model = "claude-sonnet-4-20250514"

# --- Choose at least one platform below ---

# Feishu / Lark (WebSocket, no public IP needed)
[[platforms]]
type = "feishu"

[platforms.options]
app_id = "your-feishu-app-id"
app_secret = "your-feishu-app-secret"

# For more platforms (DingTalk, Telegram, Slack, Discord, LINE, WeChat Work)
# see: https://github.com/ZacharyJia/cx-connect/blob/main/config.example.toml
`
	return os.WriteFile(path, []byte(tmpl), 0o644)
}

// createEngine creates an engine from the config.
func createEngine(cfg *config.Config) *core.Engine {
	agent, err := core.CreateAgent(cfg.Agent.Type, cfg.Agent.Options)
	if err != nil {
		slog.Error("failed to create agent", "error", err)
		os.Exit(1)
	}

	// Wire providers if the agent supports it
	if ps, ok := agent.(core.ProviderSwitcher); ok && len(cfg.Agent.Providers) > 0 {
		providers := make([]core.ProviderConfig, len(cfg.Agent.Providers))
		for i, p := range cfg.Agent.Providers {
			providers[i] = core.ProviderConfig{
				Name:    p.Name,
				APIKey:  p.APIKey,
				BaseURL: p.BaseURL,
				Model:   p.Model,
				Env:     p.Env,
			}
		}
		ps.SetProviders(providers)
		if active, _ := cfg.Agent.Options["provider"].(string); active != "" {
			ps.SetActiveProvider(active)
		}
	}

	// Parse language setting
	var lang core.Language
	switch cfg.Language {
	case "zh", "chinese":
		lang = core.LangChinese
	case "en", "english":
		lang = core.LangEnglish
	default:
		lang = core.LangAuto
	}

	var platforms []core.Platform
	for _, pc := range cfg.Platforms {
		opts := pc.Options
		if opts == nil {
			opts = map[string]any{}
		}
		if pc.Type == "telegram" {
			if _, exists := opts["language"]; !exists {
				opts["language"] = string(lang) // "en" / "zh" / ""
			}
		}
		p, err := core.CreatePlatform(pc.Type, opts)
		if err != nil {
			slog.Error("failed to create platform", "type", pc.Type, "error", err)
			os.Exit(1)
		}
		platforms = append(platforms, p)
	}

	// Session file path
	sessionFile := sessionStorePath(cfg.DataDir, "default", "")

	engine := core.NewEngine("default", agent, platforms, sessionFile, lang, cfg.AllowUsers)

	// Wire speech-to-text if enabled
	if cfg.Speech.Enabled {
		speechCfg := core.SpeechCfg{
			Enabled:  true,
			Language: cfg.Speech.Language,
		}
		switch cfg.Speech.Provider {
		case "groq":
			apiKey := cfg.Speech.Groq.APIKey
			model := cfg.Speech.Groq.Model
			if model == "" {
				model = "whisper-large-v3-turbo"
			}
			if apiKey != "" {
				speechCfg.STT = core.NewOpenAIWhisper(apiKey, "https://api.groq.com/openai/v1", model)
			} else {
				slog.Warn("speech: groq provider enabled but api_key is empty")
			}
		default:
			apiKey := cfg.Speech.OpenAI.APIKey
			baseURL := cfg.Speech.OpenAI.BaseURL
			model := cfg.Speech.OpenAI.Model
			if apiKey != "" {
				speechCfg.STT = core.NewOpenAIWhisper(apiKey, baseURL, model)
			} else {
				slog.Warn("speech: openai provider enabled but api_key is empty")
			}
		}
		if speechCfg.STT != nil {
			engine.SetSpeechConfig(speechCfg)
			slog.Info("speech: enabled", "provider", cfg.Speech.Provider)
		}
	}

	// Set up save callback for auto-detected language
	if lang == core.LangAuto {
		engine.SetLanguageSaveFunc(func(l core.Language) error {
			return config.SaveLanguage(string(l))
		})
	}

	// Set up save callbacks for provider management
	engine.SetProviderSaveFunc(func(providerName string) error {
		return config.SaveActiveProvider(providerName)
	})
	engine.SetProviderAddSaveFunc(func(p core.ProviderConfig) error {
		return config.AddProviderToConfig(config.ProviderConfig{
			Name: p.Name, APIKey: p.APIKey, BaseURL: p.BaseURL,
			Model: p.Model, Env: p.Env,
		})
	})
	engine.SetProviderRemoveSaveFunc(func(name string) error {
		return config.RemoveProviderFromConfig(name)
	})

	return engine
}

func setupLogger(level string, out io.Writer) {
	if out == nil {
		out = os.Stdout
	}
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{
		Level: logLevel,
	})))
}
