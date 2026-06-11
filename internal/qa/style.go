package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// groupStyle is the per-group voice model: a learned description of how the
// group talks, plus explicit tone directives members gave via /tone.
type groupStyle struct {
	StyleDesc string    `json:"style_desc"` // learned from chat
	Tone      string    `json:"tone"`       // member-requested directives
	MsgCount  int       `json:"msg_count"`  // messages since last style refresh
	UpdatedAt time.Time `json:"updated_at"`
}

const styleRefreshEvery = 30 // group messages between style re-learning

const styleLearnPrompt = `你在观察一个QQ群的说话风格。根据群聊记录总结这个群的语言风格画像：常用的梗和口癖、句子长短、表情/标点习惯、互怼方式、活跃话题。80字以内，写成"模仿指南"语气（如：句子短平快，爱用xx梗……）。直接输出画像文本。`

const toneMergePrompt = `你在维护一个群聊机器人的"语气指令"。把已有语气指令和群友的新要求合并成一条连贯的指令（新要求优先，冲突时覆盖旧的），100字以内，祈使句风格。直接输出合并后的指令文本，不要解释。`

func (h *Handler) styleKey(groupID int64) string {
	return fmt.Sprintf("style-%d", groupID)
}

func (h *Handler) loadStyle(groupID int64) *groupStyle {
	h.styleMu.Lock()
	defer h.styleMu.Unlock()
	if st, ok := h.styles[groupID]; ok {
		return st
	}
	st := &groupStyle{}
	_ = h.profiles.store.LoadJSON("profiles", h.styleKey(groupID), st)
	h.styles[groupID] = st
	return st
}

func (h *Handler) persistStyle(groupID int64) {
	h.styleMu.Lock()
	st := h.styles[groupID]
	h.styleMu.Unlock()
	if st == nil {
		return
	}
	if err := h.profiles.store.SaveJSON("profiles", h.styleKey(groupID), st); err != nil {
		h.logger.Error("persist style failed", "group", groupID, "error", err)
	}
}

// styleContext returns (学习到的群风格, 群友语气要求) for prompt payloads.
func (h *Handler) styleContext(groupID int64) (string, string) {
	st := h.loadStyle(groupID)
	h.styleMu.Lock()
	defer h.styleMu.Unlock()
	return st.StyleDesc, st.Tone
}

// observeStyle counts group traffic and periodically re-learns the group's
// voice from recent chat. Called for every non-command message.
func (h *Handler) observeStyle(ctx context.Context, groupID int64) {
	if h.llm == nil {
		return
	}
	st := h.loadStyle(groupID)
	h.styleMu.Lock()
	st.MsgCount++
	due := st.MsgCount >= styleRefreshEvery && !h.styleBusy[groupID]
	if due {
		st.MsgCount = 0
		h.styleBusy[groupID] = true
	}
	h.styleMu.Unlock()
	if !due {
		return
	}
	go func() {
		defer func() {
			h.styleMu.Lock()
			h.styleBusy[groupID] = false
			h.styleMu.Unlock()
		}()
		payload, err := json.Marshal(map[string]any{"群聊记录": h.recentChat(groupID)})
		if err != nil {
			return
		}
		cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		out, err := h.llm.Generate(cctx, styleLearnPrompt, string(payload))
		if err != nil {
			h.logger.Error("style learn failed", "group", groupID, "error", err)
			return
		}
		out = strings.TrimSpace(out)
		if out == "" {
			return
		}
		h.styleMu.Lock()
		st.StyleDesc = out
		st.UpdatedAt = time.Now()
		h.styleMu.Unlock()
		h.persistStyle(groupID)
		h.logger.Info("group style learned", "group", groupID, "style", out)
	}()
}

// cmdTone handles "/tone [要求|重置]".
func (h *Handler) cmdTone(ctx context.Context, groupID int64, nickname, arg string) string {
	st := h.loadStyle(groupID)
	arg = strings.TrimSpace(arg)
	switch arg {
	case "":
		h.styleMu.Lock()
		tone, style := st.Tone, st.StyleDesc
		h.styleMu.Unlock()
		if tone == "" && style == "" {
			return "🎙️ 当前没有特别的语气设定。用法：/tone 你的要求（如：说话再损一点/少用表情）；/tone 重置"
		}
		out := "🎙️ 当前语气设定：\n"
		if tone != "" {
			out += "群友要求：" + tone + "\n"
		}
		if style != "" {
			out += "学到的群风格：" + style
		}
		return strings.TrimRight(out, "\n")
	case "重置", "reset":
		h.styleMu.Lock()
		st.Tone = ""
		h.styleMu.Unlock()
		h.persistStyle(groupID)
		return "🎙️ 语气已重置，恢复出厂嘴臭"
	}

	h.styleMu.Lock()
	old := st.Tone
	h.styleMu.Unlock()
	merged := arg
	if old != "" && h.llm != nil {
		payload, _ := json.Marshal(map[string]any{"已有语气指令": old, "新要求": arg, "提出人": nickname})
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if out, err := h.llm.Generate(cctx, toneMergePrompt, string(payload)); err == nil && strings.TrimSpace(out) != "" {
			merged = strings.TrimSpace(out)
		} else {
			merged = old + "；" + arg // degrade: append
		}
	}
	h.styleMu.Lock()
	st.Tone = merged
	h.styleMu.Unlock()
	h.persistStyle(groupID)
	return "🎙️ 收到，语气已调整：" + merged
}
