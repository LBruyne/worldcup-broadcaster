package qa

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// maybeEngage decides whether the bot jumps into the conversation like a
// human group member. Two modes:
//   - followup: the bot spoke recently; judge whether the new message is a
//     reply to it (then answering is mandatory).
//   - proactive: random, rate-limited interjections on topics it can serve.
//
// The LLM signals silence by returning exactly "PASS".
func (h *Handler) maybeEngage(ctx context.Context, groupID int64, nickname, text string) {
	if h.llm == nil {
		return
	}
	h.mu.Lock()
	sinceBot := time.Since(h.lastBotMsg[groupID])
	sinceEngage := time.Since(h.lastEngage[groupID])
	busy := h.engageBusy[groupID]
	h.mu.Unlock()

	mode := ""
	switch {
	case sinceBot < h.opts.FollowupWindow:
		mode = "followup"
	case sinceEngage > h.opts.EngageCooldown && h.randFloat() < h.opts.EngageProb:
		mode = "proactive"
	}
	if mode == "" || busy {
		return
	}
	h.mu.Lock()
	if h.engageBusy[groupID] {
		h.mu.Unlock()
		return
	}
	h.engageBusy[groupID] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.engageBusy[groupID] = false
		h.mu.Unlock()
	}()

	chat := h.recentChat(groupID)
	payload, err := json.Marshal(map[string]any{
		"模式":     mode,
		"本群群名":   h.groupName(groupID),
		"最新消息":   ChatMsg{Nickname: nickname, Text: text},
		"发言者画像":  h.profiles.Persona(nickname),
		"在场成员画像": h.profiles.Known(chat),
		"群聊上下文":  chat,
		"实时数据":   json.RawMessage(h.liveData(ctx)),
	})
	if err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	out, err := h.llm.Generate(cctx, engageSystemPrompt, string(payload))
	if err != nil {
		h.logger.Error("engage llm failed", "error", err)
		return
	}
	out = strings.TrimSpace(out)
	if out == "" || strings.HasPrefix(strings.ToUpper(out), "PASS") {
		h.logger.Debug("engage pass", "mode", mode, "group", groupID)
		return
	}
	h.logger.Info("engaging in conversation", "mode", mode, "group", groupID, "trigger", nickname)
	if mode == "proactive" {
		h.mu.Lock()
		h.lastEngage[groupID] = time.Now()
		h.mu.Unlock()
	}
	h.reply(groupID, out)
}
