// Package dingbot wraps the official DingTalk Stream-mode SDK so admins can
// drive Claude Code on this repo from a DingTalk group: it connects over a
// websocket (no public URL needed), normalizes inbound @-bot messages, and
// replies back into the conversation via the per-message sessionWebhook.
package dingbot

import (
	"context"
	"log/slog"
	"strings"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
)

// InboundMsg is the normalized form of a DingTalk chatbot callback.
type InboundMsg struct {
	Text             string   // message text (mention markup already stripped by DingTalk)
	SenderStaffID    string   // stable per-user id used for the admin allowlist
	SenderNick       string   // display name
	ConversationID   string   // openConversationId
	ConversationType string   // "1" = 1:1, "2" = group
	SessionWebhook   string   // short-lived reply endpoint for this message
	IsOrgAdmin       bool     // DingTalk org-admin flag from the payload
	AtStaffIDs       []string // staffIds @-mentioned in the message
}

// IsGroup reports whether the message came from a group conversation.
func (m InboundMsg) IsGroup() bool { return m.ConversationType == "2" }

// Replier posts a markdown reply for the message being handled.
type Replier func(markdown string)

// Handler processes one normalized message. It may call reply more than once
// (e.g. an immediate ack then a final result).
type Handler func(ctx context.Context, msg InboundMsg, reply Replier)

// Client is a thin DingTalk Stream connection.
type Client struct {
	appKey    string
	appSecret string
	handler   Handler
	replier   *chatbot.ChatbotReplier
	logger    *slog.Logger
}

// New builds a Stream client. Call SetHandler before Start.
func New(appKey, appSecret string, logger *slog.Logger) *Client {
	return &Client{
		appKey:    appKey,
		appSecret: appSecret,
		replier:   chatbot.NewChatbotReplier(),
		logger:    logger,
	}
}

// SetHandler installs the message handler.
func (c *Client) SetHandler(h Handler) { c.handler = h }

// toInbound normalizes the SDK callback model. Kept separate for testing.
func toInbound(data *chatbot.BotCallbackDataModel) InboundMsg {
	m := InboundMsg{
		Text:             strings.TrimSpace(data.Text.Content),
		SenderStaffID:    data.SenderStaffId,
		SenderNick:       data.SenderNick,
		ConversationID:   data.ConversationId,
		ConversationType: data.ConversationType,
		SessionWebhook:   data.SessionWebhook,
		IsOrgAdmin:       data.IsAdmin,
	}
	for _, u := range data.AtUsers {
		if u.StaffId != "" {
			m.AtStaffIDs = append(m.AtStaffIDs, u.StaffId)
		}
	}
	return m
}

// onMessage adapts the SDK callback to our handler and supplies a markdown
// replier bound to this message's sessionWebhook.
func (c *Client) onMessage(ctx context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
	msg := toInbound(data)
	if c.handler == nil || msg.Text == "" {
		return []byte(""), nil
	}
	webhook := msg.SessionWebhook
	reply := func(markdown string) {
		if strings.TrimSpace(markdown) == "" {
			return
		}
		if err := c.replier.SimpleReplyMarkdown(ctx, webhook,
			[]byte("世界杯Bot"), []byte(markdown)); err != nil {
			c.logger.Error("dingtalk reply failed", "error", err)
		}
	}
	c.handler(ctx, msg, reply)
	return []byte(""), nil
}

// Start opens the Stream connection and blocks until ctx is cancelled or the
// connection fails. Run it in a goroutine.
func (c *Client) Start(ctx context.Context) error {
	cli := client.NewStreamClient(
		client.WithAppCredential(client.NewAppCredentialConfig(c.appKey, c.appSecret)),
		client.WithAutoReconnect(true),
	)
	cli.RegisterChatBotCallbackRouter(c.onMessage)
	if err := cli.Start(ctx); err != nil {
		return err
	}
	defer cli.Close()
	c.logger.Info("dingtalk stream connected")
	<-ctx.Done()
	return ctx.Err()
}
