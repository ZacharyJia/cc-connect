package telegram

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func init() {
	core.RegisterPlatform("telegram", New)
}

type replyContext struct {
	chatID    int64
	messageID int
}

type Platform struct {
	token    string
	language string // "en" / "zh" / ""(auto)
	bot      *tgbotapi.BotAPI
	handler  core.MessageHandler
	cancel   context.CancelFunc
}

func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("telegram: token is required")
	}
	lang, _ := opts["language"].(string)
	lang = strings.ToLower(strings.TrimSpace(lang))
	switch lang {
	case "zh", "en":
		// keep as is
	default:
		lang = ""
	}
	return &Platform{token: token, language: lang}, nil
}

func (p *Platform) Name() string { return "telegram" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	bot, err := tgbotapi.NewBotAPI(p.token)
	if err != nil {
		return fmt.Errorf("telegram: auth failed: %w", err)
	}
	p.bot = bot

	slog.Info("telegram: connected", "bot", bot.Self.UserName)
	p.registerCommands()

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := bot.GetUpdatesChan(u)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case update := <-updates:
				// Handle callback query (button clicks)
				if update.CallbackQuery != nil {
					cbq := update.CallbackQuery
					userName := cbq.From.UserName
					if userName == "" {
						userName = strings.TrimSpace(cbq.From.FirstName + " " + cbq.From.LastName)
					}

					// Answer callback query first (required to show buttons)
					cbqConfig := tgbotapi.CallbackConfig{
						CallbackQueryID: cbq.ID,
						ShowAlert:       false,
					}
					p.bot.Request(cbqConfig)

					// Callback query might not have Message in some cases
					chatID := cbq.From.ID
					messageID := 0
					if cbq.Message != nil {
						chatID = cbq.Message.Chat.ID
						messageID = cbq.Message.MessageID
					}

					sessionKey := fmt.Sprintf("telegram:%d:%d", chatID, cbq.From.ID)
					rctx := replyContext{chatID: chatID, messageID: messageID}

					coreMsg := &core.Message{
						SessionKey: sessionKey,
						Platform:   "telegram",
						UserID:     strconv.FormatInt(cbq.From.ID, 10),
						UserName:   userName,
						Content:    cbq.Data, // Button callback data as content
						ReplyCtx:   rctx,
					}
					p.handler(p, coreMsg)
					continue
				}

				if update.Message == nil {
					continue
				}

				msg := update.Message
				userName := msg.From.UserName
				if userName == "" {
					userName = strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
				}
				sessionKey := fmt.Sprintf("telegram:%d:%d", msg.Chat.ID, msg.From.ID)
				rctx := replyContext{chatID: msg.Chat.ID, messageID: msg.MessageID}

				// Handle photo messages
				if msg.Photo != nil && len(msg.Photo) > 0 {
					best := msg.Photo[len(msg.Photo)-1]
					imgData, err := p.downloadFile(best.FileID)
					if err != nil {
						slog.Error("telegram: download photo failed", "error", err)
						continue
					}
					coreMsg := &core.Message{
						SessionKey: sessionKey, Platform: "telegram",
						UserID: strconv.FormatInt(msg.From.ID, 10), UserName: userName,
						Content:  msg.Caption,
						Images:   []core.ImageAttachment{{MimeType: "image/jpeg", Data: imgData}},
						ReplyCtx: rctx,
					}
					p.handler(p, coreMsg)
					continue
				}

				// Handle voice messages
				if msg.Voice != nil {
					slog.Debug("telegram: voice received", "user", userName, "duration", msg.Voice.Duration)
					audioData, err := p.downloadFile(msg.Voice.FileID)
					if err != nil {
						slog.Error("telegram: download voice failed", "error", err)
						continue
					}
					coreMsg := &core.Message{
						SessionKey: sessionKey, Platform: "telegram",
						UserID: strconv.FormatInt(msg.From.ID, 10), UserName: userName,
						Audio: &core.AudioAttachment{
							MimeType: msg.Voice.MimeType,
							Data:     audioData,
							Format:   "ogg",
							Duration: msg.Voice.Duration,
						},
						ReplyCtx: rctx,
					}
					p.handler(p, coreMsg)
					continue
				}

				// Handle audio file messages
				if msg.Audio != nil {
					slog.Debug("telegram: audio file received", "user", userName)
					audioData, err := p.downloadFile(msg.Audio.FileID)
					if err != nil {
						slog.Error("telegram: download audio failed", "error", err)
						continue
					}
					format := "mp3"
					if msg.Audio.MimeType != "" {
						parts := strings.SplitN(msg.Audio.MimeType, "/", 2)
						if len(parts) == 2 {
							format = parts[1]
						}
					}
					coreMsg := &core.Message{
						SessionKey: sessionKey, Platform: "telegram",
						UserID: strconv.FormatInt(msg.From.ID, 10), UserName: userName,
						Audio: &core.AudioAttachment{
							MimeType: msg.Audio.MimeType,
							Data:     audioData,
							Format:   format,
							Duration: msg.Audio.Duration,
						},
						ReplyCtx: rctx,
					}
					p.handler(p, coreMsg)
					continue
				}

				if msg.Text == "" {
					continue
				}

				text := msg.Text
				if p.bot.Self.UserName != "" {
					text = strings.Replace(text, "@"+p.bot.Self.UserName, "", 1)
				}

				coreMsg := &core.Message{
					SessionKey: sessionKey, Platform: "telegram",
					UserID: strconv.FormatInt(msg.From.ID, 10), UserName: userName,
					Content: text, ReplyCtx: rctx,
				}

				slog.Debug("telegram: message received", "user", userName, "chat", msg.Chat.ID)
				p.handler(p, coreMsg)
			}
		}
	}()

	return nil
}

func (p *Platform) registerCommands() {
	en := []tgbotapi.BotCommand{
		{Command: "new", Description: "Start a new session"},
		{Command: "list", Description: "List sessions"},
		{Command: "switch", Description: "Switch session"},
		{Command: "current", Description: "Show current session"},
		{Command: "history", Description: "Show recent messages"},
		{Command: "provider", Description: "Manage providers"},
		{Command: "allow", Description: "Pre-allow a tool"},
		{Command: "mode", Description: "View or switch mode"},
		{Command: "lang", Description: "View or switch language"},
		{Command: "quiet", Description: "Toggle progress messages"},
		{Command: "stop", Description: "Stop current execution"},
		{Command: "cron", Description: "Manage scheduled tasks"},
		{Command: "version", Description: "Show cc-connect version"},
		{Command: "help", Description: "Show help"},
	}

	zh := []tgbotapi.BotCommand{
		{Command: "new", Description: "创建新会话"},
		{Command: "list", Description: "列出会话"},
		{Command: "switch", Description: "切换会话"},
		{Command: "current", Description: "当前会话"},
		{Command: "history", Description: "最近消息"},
		{Command: "provider", Description: "管理提供商"},
		{Command: "allow", Description: "预授权工具"},
		{Command: "mode", Description: "查看或切换模式"},
		{Command: "lang", Description: "查看或切换语言"},
		{Command: "quiet", Description: "开关进度消息"},
		{Command: "stop", Description: "停止当前执行"},
		{Command: "cron", Description: "管理定时任务"},
		{Command: "version", Description: "查看版本"},
		{Command: "help", Description: "帮助"},
	}

	switch p.language {
	case "zh":
		// Config language is zh: make Chinese the default command list.
		if _, err := p.bot.Request(tgbotapi.NewSetMyCommands(zh...)); err != nil {
			slog.Warn("telegram: failed to set zh bot commands", "error", err)
			return
		}
		slog.Info("telegram: bot commands registered", "count", len(zh), "language", "zh")
	case "en":
		if _, err := p.bot.Request(tgbotapi.NewSetMyCommands(en...)); err != nil {
			slog.Warn("telegram: failed to set bot commands", "error", err)
			return
		}
		slog.Info("telegram: bot commands registered", "count", len(en), "language", "en")
	default:
		// Auto/default: English as default, plus zh override for zh clients.
		if _, err := p.bot.Request(tgbotapi.NewSetMyCommands(en...)); err != nil {
			slog.Warn("telegram: failed to set bot commands", "error", err)
			return
		}
		scope := tgbotapi.NewBotCommandScopeDefault()
		if _, err := p.bot.Request(tgbotapi.NewSetMyCommandsWithScopeAndLanguage(scope, "zh", zh...)); err != nil {
			slog.Warn("telegram: failed to set zh bot commands", "error", err)
		}
		slog.Info("telegram: bot commands registered", "count", len(en), "language", "auto")
	}
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}

	reply := tgbotapi.NewMessage(rc.chatID, content)
	reply.ReplyToMessageID = rc.messageID
	reply.ParseMode = tgbotapi.ModeMarkdown

	if _, err := p.bot.Send(reply); err != nil {
		// Markdown parse failure → retry as plain text
		if strings.Contains(err.Error(), "can't parse") {
			reply.ParseMode = ""
			_, err = p.bot.Send(reply)
		}
		if err != nil {
			return fmt.Errorf("telegram: send: %w", err)
		}
	}
	return nil
}

// ReplyWithButtons sends a message with inline keyboard buttons.
func (p *Platform) ReplyWithButtons(ctx context.Context, rctx any, content string, buttons []core.Button) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}

	reply := tgbotapi.NewMessage(rc.chatID, content)
	reply.ParseMode = tgbotapi.ModeMarkdown
	reply.ReplyToMessageID = rc.messageID

	// Convert buttons to inline keyboard
	var keyboard [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton
	for i, btn := range buttons {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(btn.Text, btn.Data))
		// Create new row every 2 buttons or at the end
		if len(row) == 2 || i == len(buttons)-1 {
			keyboard = append(keyboard, row)
			row = nil
		}
	}
	reply.ReplyMarkup = tgbotapi.InlineKeyboardMarkup{InlineKeyboard: keyboard}

	slog.Debug("telegram: sending message with buttons", "chat_id", rc.chatID, "buttons", len(buttons))

	if _, err := p.bot.Send(reply); err != nil {
		// Markdown parse failure → retry as plain text
		if strings.Contains(err.Error(), "can't parse") {
			reply.ParseMode = ""
			_, err = p.bot.Send(reply)
		}
		if err != nil {
			return fmt.Errorf("telegram: send with buttons: %w", err)
		}
	}
	return nil
}

// Send sends a new message (not a reply)
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}

	msg := tgbotapi.NewMessage(rc.chatID, content)
	msg.ParseMode = tgbotapi.ModeMarkdown

	if _, err := p.bot.Send(msg); err != nil {
		// Markdown parse failure → retry as plain text
		if strings.Contains(err.Error(), "can't parse") {
			msg.ParseMode = ""
			_, err = p.bot.Send(msg)
		}
		if err != nil {
			return fmt.Errorf("telegram: send: %w", err)
		}
	}
	return nil
}

func (p *Platform) downloadFile(fileID string) ([]byte, error) {
	fileConfig := tgbotapi.FileConfig{FileID: fileID}
	file, err := p.bot.GetFile(fileConfig)
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	link := file.Link(p.bot.Token)

	resp, err := http.Get(link)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// telegram:{chatID}:{userID}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "telegram" {
		return nil, fmt.Errorf("telegram: invalid session key %q", sessionKey)
	}
	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("telegram: invalid chat ID in %q", sessionKey)
	}
	return replyContext{chatID: chatID}, nil
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.bot != nil {
		p.bot.StopReceivingUpdates()
	}
	return nil
}
