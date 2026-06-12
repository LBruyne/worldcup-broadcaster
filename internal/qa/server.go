package qa

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// onebotEvent is the subset of an OneBot 11 push event we care about.
type onebotEvent struct {
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type"`
	NoticeType  string `json:"notice_type"`
	SelfID      int64  `json:"self_id"`
	GroupID     int64  `json:"group_id"`
	UserID      int64  `json:"user_id"`
	RawMessage  string `json:"raw_message"`
	Sender      struct {
		Nickname string `json:"nickname"`
		Card     string `json:"card"`
	} `json:"sender"`
	Message json.RawMessage `json:"message"`
}

// messageText extracts plain text from either segment-array or string
// message formats. A real @-mention of the bot arrives as an "at" segment
// (not text): it is rewritten to the bot's name so the engagement engine
// treats it exactly like being called by name.
func (e *onebotEvent) messageText() string {
	var segs []struct {
		Type string `json:"type"`
		Data struct {
			Text string `json:"text"`
			QQ   string `json:"qq"`
		} `json:"data"`
	}
	if err := json.Unmarshal(e.Message, &segs); err == nil {
		atSelf := false
		var b strings.Builder
		for _, s := range segs {
			switch s.Type {
			case "text":
				b.WriteString(s.Data.Text)
			case "at":
				if s.Data.QQ == strconv.FormatInt(e.SelfID, 10) {
					atSelf = true
				}
			}
		}
		if b.Len() > 0 {
			text := b.String()
			if atSelf && !strings.Contains(text, BotName) {
				text = "@" + BotName + " " + strings.TrimSpace(text)
			}
			return text
		}
	}
	var str string
	if err := json.Unmarshal(e.Message, &str); err == nil && str != "" {
		return str
	}
	return e.RawMessage
}

func (e *onebotEvent) displayName() string {
	if e.Sender.Card != "" {
		return e.Sender.Card
	}
	if e.Sender.Nickname != "" {
		return e.Sender.Nickname
	}
	return "群友"
}

// StartServer listens for NapCat event pushes and feeds group messages into
// the handler. It shuts down when ctx is cancelled.
func StartServer(ctx context.Context, addr string, h *Handler, logger *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		defer w.WriteHeader(http.StatusNoContent)
		if r.Method != http.MethodPost {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			return
		}
		var ev onebotEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			logger.Debug("unparsable event", "error", err)
			return
		}
		// The bot being added to a group triggers an instant self-intro.
		if ev.PostType == "notice" && ev.NoticeType == "group_increase" && ev.UserID == ev.SelfID {
			go h.OnSelfJoin(ev.GroupID)
			return
		}
		if ev.PostType != "message" || ev.MessageType != "group" {
			return
		}
		// Commands may hit ESPN/LLM; never block NapCat's push loop.
		go h.OnGroupMessage(ctx, ev.GroupID, ev.UserID, ev.displayName(), ev.messageText())
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	logger.Info("qa event server listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
