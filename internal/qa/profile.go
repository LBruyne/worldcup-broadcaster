package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"worldcup-broadcaster/internal/store"
)

// Profile is the bot's evolving mental model of one group member.
type Profile struct {
	Nickname  string    `json:"nickname"`
	Persona   string    `json:"persona"`
	Seeded    bool      `json:"seeded"`    // came from config, never fully overwritten
	MsgCount  int       `json:"msg_count"` // messages since last persona refresh
	Recent    []string  `json:"recent"`    // their last few messages
	UpdatedAt time.Time `json:"updated_at"`
}

// Profiles maintains per-user personas, persisted to data/profiles/users.json.
type Profiles struct {
	mu    sync.Mutex
	store *store.Store
	llm   LLM
	log   *slog.Logger

	users    map[string]*Profile // keyed by nickname
	updating map[string]bool     // single-flight persona refresh per user
}

const (
	profileRefreshEvery = 8  // their messages between persona refreshes
	profileRecentKeep   = 15 // their messages kept for the refresh prompt
)

const personaUpdatePrompt = `你在维护一个QQ群成员的"人设画像"。根据已有画像、该成员的最新发言、以及群聊上下文（包括别人怎么跟他互动），输出更新后的画像：80字以内，突出球队立场、喜恶、口头禅、职业/背景、和谁不对付。保留已有画像中依然成立的信息。直接输出画像文本，不要解释。`

func NewProfiles(st *store.Store, llm LLM, seeds map[string]string, log *slog.Logger) *Profiles {
	p := &Profiles{
		store: st, llm: llm, log: log,
		users:    make(map[string]*Profile),
		updating: make(map[string]bool),
	}
	// load persisted profiles first, then overlay seeds (seeds win on persona
	// text only if the user has no learned persona yet)
	var saved map[string]*Profile
	if err := st.LoadJSON("profiles", "users", &saved); err == nil {
		for k, v := range saved {
			p.users[k] = v
		}
	}
	for nick, persona := range seeds {
		if existing, ok := p.users[nick]; ok {
			existing.Seeded = true
			if !strings.Contains(existing.Persona, persona) && existing.Persona == "" {
				existing.Persona = persona
			}
			continue
		}
		p.users[nick] = &Profile{Nickname: nick, Persona: persona, Seeded: true, UpdatedAt: time.Now()}
	}
	return p
}

func (p *Profiles) persistLocked() {
	if err := p.store.SaveJSON("profiles", "users", p.users); err != nil {
		p.log.Error("persist profiles failed", "error", err)
	}
}

// Record stores one message by nickname and kicks off an async persona
// refresh when enough new material accumulated.
func (p *Profiles) Record(ctx context.Context, nickname, text string, groupContext []ChatMsg) {
	p.mu.Lock()
	prof, ok := p.users[nickname]
	if !ok {
		prof = &Profile{Nickname: nickname}
		p.users[nickname] = prof
	}
	prof.MsgCount++
	prof.Recent = append(prof.Recent, text)
	if len(prof.Recent) > profileRecentKeep {
		prof.Recent = prof.Recent[len(prof.Recent)-profileRecentKeep:]
	}
	needRefresh := prof.MsgCount >= profileRefreshEvery && p.llm != nil && !p.updating[nickname]
	if needRefresh {
		p.updating[nickname] = true
		prof.MsgCount = 0
	}
	p.persistLocked()
	p.mu.Unlock()

	if needRefresh {
		go p.refresh(ctx, nickname, groupContext)
	}
}

func (p *Profiles) refresh(ctx context.Context, nickname string, groupContext []ChatMsg) {
	defer func() {
		p.mu.Lock()
		p.updating[nickname] = false
		p.mu.Unlock()
	}()
	p.mu.Lock()
	prof := p.users[nickname]
	payload, err := json.Marshal(map[string]any{
		"成员昵称":   nickname,
		"已有画像":   prof.Persona,
		"他的最新发言": prof.Recent,
		"群聊上下文":  groupContext,
	})
	p.mu.Unlock()
	if err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	out, err := p.llm.Generate(cctx, personaUpdatePrompt, string(payload))
	if err != nil {
		p.log.Error("persona refresh failed", "user", nickname, "error", err)
		return
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return
	}
	p.mu.Lock()
	prof.Persona = out
	prof.UpdatedAt = time.Now()
	p.persistLocked()
	p.mu.Unlock()
	p.log.Info("persona refreshed", "user", nickname, "persona", out)
}

// Ensure creates an empty profile stub if the nickname is unknown.
func (p *Profiles) Ensure(nickname string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.users[nickname]; !ok {
		p.users[nickname] = &Profile{Nickname: nickname}
		p.persistLocked()
	}
}

// SetPersona overrides a persona (admin command); marks it seeded so the
// learned refinements build on top of it.
func (p *Profiles) SetPersona(nickname, persona string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	prof, ok := p.users[nickname]
	if !ok {
		prof = &Profile{Nickname: nickname}
		p.users[nickname] = prof
	}
	prof.Persona = persona
	prof.Seeded = true
	prof.UpdatedAt = time.Now()
	p.persistLocked()
}

// Persona returns the current persona text for a nickname ("" if unknown).
func (p *Profiles) Persona(nickname string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prof, ok := p.users[nickname]; ok {
		return prof.Persona
	}
	return ""
}

// Known returns the personas of everyone appearing in the chat slice, as a
// nickname->persona map ready for the LLM payload.
func (p *Profiles) Known(chat []ChatMsg) map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]string{}
	for _, m := range chat {
		if prof, ok := p.users[m.Nickname]; ok && prof.Persona != "" {
			out[m.Nickname] = prof.Persona
		}
	}
	return out
}

// Describe is used in logs/debug.
func (p *Profiles) Describe(nickname string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	prof, ok := p.users[nickname]
	if !ok {
		return "<unknown>"
	}
	return fmt.Sprintf("%s: %s", prof.Nickname, prof.Persona)
}
