package qa

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolymarketOddsAttachedToOddsQuestion(t *testing.T) {
	pm := filepath.Join(t.TempDir(), "wc.json")
	if err := os.WriteFile(pm, []byte(`{"source":"Polymarket","updated":"2026-06-14T03:30Z","markets":{"夺冠概率":[{"name":"Argentina","prob":0.078},{"name":"Spain","prob":0.166}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	llm := &routerLLM{route: `{"needs":[],"difficulty":"easy","search":""}`, answer: "阿根廷盘口约7.8%"}
	h, sender := newHandler(t, llm)
	h.opts.PolymarketFile = pm
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/ask 阿根廷夺冠概率多大")
	deadlineWait(t, sender, 1)
	if !strings.Contains(llm.askUser, "Polymarket") || !strings.Contains(llm.askUser, "Argentina") {
		t.Errorf("odds question must carry Polymarket data:\n%.300s", llm.askUser)
	}
}

func TestPolymarketSkippedForNonOdds(t *testing.T) {
	pm := filepath.Join(t.TempDir(), "wc.json")
	os.WriteFile(pm, []byte(`{"markets":{"夺冠概率":[{"name":"Argentina","prob":0.078}]}}`), 0o644)
	llm := &routerLLM{route: `{"needs":[],"difficulty":"easy","search":""}`, answer: "在呢"}
	h, sender := newHandler(t, llm)
	h.opts.PolymarketFile = pm
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/ask 你叫什么名字")
	deadlineWait(t, sender, 1)
	if strings.Contains(llm.askUser, "Polymarket") {
		t.Errorf("non-odds question must not carry Polymarket data:\n%.200s", llm.askUser)
	}
}

func TestPolymarketReaderEdgeCases(t *testing.T) {
	h, _ := newHandler(t, nil)
	h.opts.PolymarketFile = "/nonexistent/wc.json"
	if h.polymarketOdds() != "" {
		t.Error("missing file must return empty")
	}
	h.opts.PolymarketFile = ""
	if h.polymarketOdds() != "" {
		t.Error("empty path must return empty")
	}
	// malformed / empty-markets json returns empty
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"markets":{}}`), 0o644)
	h.opts.PolymarketFile = bad
	if h.polymarketOdds() != "" {
		t.Error("empty-markets json must return empty")
	}
}

func TestOddsQuestionRe(t *testing.T) {
	for _, q := range []string{"阿根廷夺冠概率", "西班牙赔率多少", "巴西出线几成", "谁能拿金靴"} {
		if !oddsQuestionRe.MatchString(q) {
			t.Errorf("%q should match oddsQuestionRe", q)
		}
	}
	for _, q := range []string{"今天有什么比赛", "梅西多大了"} {
		if oddsQuestionRe.MatchString(q) {
			t.Errorf("%q should NOT match oddsQuestionRe", q)
		}
	}
}
