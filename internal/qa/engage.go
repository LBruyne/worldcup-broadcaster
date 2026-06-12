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

	// In assistant mode the bot is deliberately quiet: proactive
	// interjections are rare and the prompt only allows data corrections.
	prob := h.opts.EngageProb
	if h.personaMode(groupID) == ModeAssistant {
		prob *= 0.2
	}
	mode := ""
	switch {
	case addressesBot(text):
		mode = "called" // named directly: always evaluate
	case sinceBot < h.opts.FollowupWindow:
		mode = "followup"
	case sinceEngage > h.opts.EngageCooldown && h.randFloat() < prob:
		mode = "proactive"
	}
	// While muted in this group every send fails anyway — stay silent
	// instead of hammering the API (and burning LLM calls).
	if mode != "" {
		if mc, ok := h.sender.(muteChecker); ok && mc.Muted(groupID) {
			h.logger.Info("engage skipped (muted in group)", "mode", mode, "group", groupID)
			return
		}
	}
	if mode == "" || busy {
		if mode != "" {
			h.logger.Info("engage skipped (busy)", "mode", mode, "group", groupID)
		}
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
	styleDesc, tone := h.styleContext(groupID)
	fields := map[string]any{
		"模式":     mode,
		"自我改进要点": h.reflectionNotes(groupID),
		"本群群名":   h.groupName(groupID),
		"本群说话风格": styleDesc,
		"群友语气要求": tone,
		"最新消息":   ChatMsg{Nickname: nickname, Text: text},
		"发言者画像":  h.profiles.Persona(nickname),
		"在场成员画像": h.profiles.Known(chat),
		"群聊上下文":  chat,
	}
	// Being addressed directly (called) gets the full /ask grounding
	// unconditionally — identical perception for @ and /ask. Follow-ups
	// run it when they look factual; plain banter keeps the cheap
	// snapshot.
	think := false
	if mode == "called" ||
		(mode == "followup" && (factualQuestionRe.MatchString(text) || deicticMatchRe.MatchString(text))) {
		grounded, hard := h.grounding(ctx, text)
		fields["今天"] = time.Now().UTC().Add(8 * time.Hour).Format("2006-01-02") + "（北京时间）"
		fields["已核实数据"] = json.RawMessage(grounded)
		think = hard
	} else {
		fields["实时数据"] = json.RawMessage(h.liveData(ctx))
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	var out string
	if tl, ok := h.llm.(ThinkingLLM); ok {
		out, err = tl.GenerateThink(cctx, h.engagePrompt(groupID), string(payload), think)
	} else {
		out, err = h.llm.Generate(cctx, h.engagePrompt(groupID), string(payload))
	}
	if err != nil {
		h.logger.Error("engage llm failed", "error", err)
		return
	}
	out = strings.TrimSpace(out)
	if out == "" || strings.HasPrefix(strings.ToUpper(out), "PASS") {
		h.logger.Info("engage pass", "mode", mode, "group", groupID, "trigger", nickname)
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

// addressesBot reports whether a plain message is clearly directed at the
// bot by name, forcing an evaluation regardless of window or probability.
func addressesBot(text string) bool {
	for _, kw := range []string{BotName, "稳定盈利", "AAA", "机器人", "bot", "Bot", "BOT", "暴龙", "罗哥迷弟", "回答上面", "回答一下"} {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}
