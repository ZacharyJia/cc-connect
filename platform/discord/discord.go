package discord

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ZacharyJia/cx-connect/core"

	"github.com/bwmarrin/discordgo"
)

func init() {
	core.RegisterPlatform("discord", New)
}

const maxDiscordLen = 2000

type replyContext struct {
	channelID   string
	messageID   string
	interaction *discordgo.Interaction
}

type Platform struct {
	token   string
	session *discordgo.Session
	handler core.MessageHandler
	botID   string
	appID   string
}

func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("discord: token is required")
	}
	return &Platform{token: token}, nil
}

func (p *Platform) Name() string { return "discord" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	session, err := discordgo.New("Bot " + p.token)
	if err != nil {
		return fmt.Errorf("discord: create session: %w", err)
	}
	p.session = session

	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		p.botID = r.User.ID
		if app, err := s.Application("@me"); err == nil && app != nil && app.ID != "" {
			p.appID = app.ID
		} else {
			// Bot user ID usually equals app ID; keep this fallback for registration.
			p.appID = r.User.ID
			if err != nil {
				slog.Warn("discord: failed to fetch application id", "error", err)
			}
		}
		slog.Info("discord: connected", "bot", r.User.Username+"#"+r.User.Discriminator)

		guildIDs := make([]string, 0, len(r.Guilds))
		for _, g := range r.Guilds {
			guildIDs = append(guildIDs, g.ID)
		}
		p.registerGuildCommands(guildIDs)
	})

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.Bot || m.Author.ID == p.botID {
			return
		}

		slog.Debug("discord: message received", "user", m.Author.Username, "channel", m.ChannelID)

		sessionKey := fmt.Sprintf("discord:%s:%s", m.ChannelID, m.Author.ID)
		rctx := replyContext{channelID: m.ChannelID, messageID: m.ID}

		var images []core.ImageAttachment
		var audio *core.AudioAttachment
		for _, att := range m.Attachments {
			ct := strings.ToLower(att.ContentType)
			if strings.HasPrefix(ct, "audio/") {
				data, err := downloadURL(att.URL)
				if err != nil {
					slog.Error("discord: download audio failed", "url", att.URL, "error", err)
					continue
				}
				format := "ogg"
				if parts := strings.SplitN(ct, "/", 2); len(parts) == 2 {
					format = parts[1]
				}
				audio = &core.AudioAttachment{
					MimeType: ct, Data: data, Format: format,
				}
			} else if att.Width > 0 && att.Height > 0 {
				data, err := downloadURL(att.URL)
				if err != nil {
					slog.Error("discord: download attachment failed", "url", att.URL, "error", err)
					continue
				}
				images = append(images, core.ImageAttachment{
					MimeType: att.ContentType, Data: data, FileName: att.Filename,
				})
			}
		}

		if m.Content == "" && len(images) == 0 && audio == nil {
			return
		}

		msg := &core.Message{
			SessionKey: sessionKey, Platform: "discord",
			UserID: m.Author.ID, UserName: m.Author.Username,
			Content: m.Content, Images: images, Audio: audio, ReplyCtx: rctx,
		}
		p.handler(p, msg)
	})

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}
		data := i.ApplicationCommandData()
		if data.Name == "" {
			return
		}

		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		}); err != nil {
			slog.Error("discord: failed to acknowledge interaction", "command", data.Name, "error", err)
			return
		}

		userID, userName := interactionUser(i)
		content := "/" + data.Name
		if args := parseInteractionArgs(data.Options); args != "" {
			content += " " + args
		}

		msg := &core.Message{
			SessionKey: fmt.Sprintf("discord:%s:%s", i.ChannelID, userID),
			Platform:   "discord",
			UserID:     userID,
			UserName:   userName,
			Content:    content,
			ReplyCtx: replyContext{
				channelID:   i.ChannelID,
				interaction: i.Interaction,
			},
		}
		p.handler(p, msg)
	})

	if err := session.Open(); err != nil {
		return fmt.Errorf("discord: open gateway: %w", err)
	}

	return nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("discord: invalid reply context type %T", rctx)
	}

	// Discord has a 2000 char limit per message
	for len(content) > 0 {
		chunk := content
		if len(chunk) > maxDiscordLen {
			// Try to split at a newline
			cut := maxDiscordLen
			if idx := lastIndexBefore(content, '\n', cut); idx > 0 {
				cut = idx + 1
			}
			chunk = content[:cut]
			content = content[cut:]
		} else {
			content = ""
		}

		var err error
		if rc.interaction != nil {
			_, err = p.session.FollowupMessageCreate(rc.interaction, false, &discordgo.WebhookParams{
				Content: chunk,
			})
		} else if rc.messageID != "" {
			ref := &discordgo.MessageReference{MessageID: rc.messageID}
			_, err = p.session.ChannelMessageSendReply(rc.channelID, chunk, ref)
		} else {
			_, err = p.session.ChannelMessageSend(rc.channelID, chunk)
		}
		if err != nil {
			return fmt.Errorf("discord: send: %w", err)
		}
	}
	return nil
}

// ReplyWithButtons is not supported in Discord
func (p *Platform) ReplyWithButtons(ctx context.Context, rctx any, content string, buttons []core.Button) error {
	return core.ErrNotSupported
}

// Send sends a new message (not a reply)
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("discord: invalid reply context type %T", rctx)
	}

	// Discord has a 2000 char limit per message
	for len(content) > 0 {
		chunk := content
		if len(chunk) > maxDiscordLen {
			cut := maxDiscordLen
			if idx := lastIndexBefore(content, '\n', cut); idx > 0 {
				cut = idx + 1
			}
			chunk = content[:cut]
			content = content[cut:]
		} else {
			content = ""
		}

		var err error
		if rc.interaction != nil {
			_, err = p.session.FollowupMessageCreate(rc.interaction, false, &discordgo.WebhookParams{
				Content: chunk,
			})
		} else {
			_, err = p.session.ChannelMessageSend(rc.channelID, chunk)
		}
		if err != nil {
			return fmt.Errorf("discord: send: %w", err)
		}
	}
	return nil
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// discord:{channelID}:{userID}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "discord" {
		return nil, fmt.Errorf("discord: invalid session key %q", sessionKey)
	}
	return replyContext{channelID: parts[1]}, nil
}

func (p *Platform) Stop() error {
	if p.session != nil {
		return p.session.Close()
	}
	return nil
}

func downloadURL(u string) ([]byte, error) {
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func lastIndexBefore(s string, b byte, before int) int {
	for i := before - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func (p *Platform) registerGuildCommands(guildIDs []string) {
	if p.appID == "" {
		slog.Warn("discord: skip slash command registration, app id is empty")
		return
	}
	if len(guildIDs) == 0 {
		slog.Info("discord: no guilds found, skip slash command registration")
		return
	}

	commands := discordSlashCommands()
	registeredGuilds := 0
	for _, guildID := range guildIDs {
		if err := p.syncGuildCommands(guildID, commands); err != nil {
			slog.Warn("discord: failed to sync guild slash commands", "guild_id", guildID, "error", err)
			continue
		}
		registeredGuilds++
	}

	slog.Info("discord: slash commands registered",
		"guilds", registeredGuilds,
		"commands_per_guild", len(commands))
}

func (p *Platform) syncGuildCommands(guildID string, desired []*discordgo.ApplicationCommand) error {
	existing, err := p.session.ApplicationCommands(p.appID, guildID)
	if err != nil {
		return fmt.Errorf("list existing commands: %w", err)
	}

	existingByName := make(map[string]*discordgo.ApplicationCommand, len(existing))
	for _, cmd := range existing {
		existingByName[cmd.Name] = cmd
	}

	desiredByName := make(map[string]struct{}, len(desired))
	var firstErr error

	for _, cmd := range desired {
		desiredByName[cmd.Name] = struct{}{}
		if old, ok := existingByName[cmd.Name]; ok {
			_, err = p.session.ApplicationCommandEdit(p.appID, guildID, old.ID, cmd)
		} else {
			_, err = p.session.ApplicationCommandCreate(p.appID, guildID, cmd)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			slog.Warn("discord: failed to upsert slash command",
				"guild_id", guildID,
				"name", cmd.Name,
				"error", err)
		}
	}

	for _, cmd := range existing {
		if _, keep := desiredByName[cmd.Name]; keep {
			continue
		}
		if err := p.session.ApplicationCommandDelete(p.appID, guildID, cmd.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			slog.Warn("discord: failed to delete stale slash command",
				"guild_id", guildID,
				"name", cmd.Name,
				"error", err)
		}
	}

	return firstErr
}

func discordSlashCommands() []*discordgo.ApplicationCommand {
	withArgs := func(name, desc string) *discordgo.ApplicationCommand {
		return &discordgo.ApplicationCommand{
			Name:        name,
			Description: desc,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "args",
					Description: "Optional command arguments",
					Required:    false,
				},
			},
		}
	}

	return []*discordgo.ApplicationCommand{
		withArgs("new", "Start a new session"),
		withArgs("list", "List sessions"),
		withArgs("switch", "Switch session"),
		withArgs("current", "Show current session"),
		withArgs("history", "Show recent messages"),
		withArgs("provider", "Manage providers"),
		withArgs("allow", "Pre-allow a tool"),
		withArgs("mode", "View or switch mode"),
		withArgs("output", "View or switch output mode"),
		withArgs("lang", "View or switch language"),
		withArgs("quiet", "Toggle progress messages"),
		withArgs("stop", "Stop current execution"),
		withArgs("cron", "Manage scheduled tasks"),
		withArgs("version", "Show cx-connect version"),
		withArgs("help", "Show help"),
	}
}

func parseInteractionArgs(options []*discordgo.ApplicationCommandInteractionDataOption) string {
	for _, opt := range options {
		if opt.Name != "args" {
			continue
		}
		if v, ok := opt.Value.(string); ok {
			return strings.TrimSpace(v)
		}
		if opt.Value != nil {
			return strings.TrimSpace(fmt.Sprint(opt.Value))
		}
	}
	return ""
}

func interactionUser(i *discordgo.InteractionCreate) (string, string) {
	if i.Member != nil && i.Member.User != nil {
		u := i.Member.User
		return u.ID, discordUserName(u)
	}
	if i.User != nil {
		return i.User.ID, discordUserName(i.User)
	}
	return "unknown", "unknown"
}

func discordUserName(u *discordgo.User) string {
	if u == nil {
		return "unknown"
	}
	if u.Username != "" {
		return u.Username
	}
	return u.ID
}
