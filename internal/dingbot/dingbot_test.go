package dingbot

import (
	"testing"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
)

func TestToInbound(t *testing.T) {
	data := &chatbot.BotCallbackDataModel{
		ConversationId:   "cidXYZ",
		ConversationType: "2",
		SenderStaffId:    "manager01",
		SenderNick:       "铁哥",
		SessionWebhook:   "https://oapi.dingtalk.com/robot/sendBySession?session=abc",
		IsAdmin:          true,
	}
	data.Text.Content = "  做 给 README 加一行  "
	data.AtUsers = []chatbot.BotCallbackDataAtUserModel{
		{StaffId: "botStaff"}, {DingtalkId: "dt1"},
	}

	m := toInbound(data)
	if m.Text != "做 给 README 加一行" {
		t.Errorf("text = %q (want trimmed)", m.Text)
	}
	if !m.IsGroup() {
		t.Error("conversationType 2 must be group")
	}
	if m.SenderStaffID != "manager01" || m.SenderNick != "铁哥" {
		t.Errorf("sender = %+v", m)
	}
	if m.SessionWebhook == "" || m.ConversationID != "cidXYZ" {
		t.Errorf("routing fields = %+v", m)
	}
	if !m.IsOrgAdmin {
		t.Error("IsOrgAdmin should reflect payload IsAdmin")
	}
	if len(m.AtStaffIDs) != 1 || m.AtStaffIDs[0] != "botStaff" {
		t.Errorf("AtStaffIDs = %v (only staffId-bearing @users)", m.AtStaffIDs)
	}
}

func TestToInboundSingleChat(t *testing.T) {
	data := &chatbot.BotCallbackDataModel{ConversationType: "1"}
	data.Text.Content = "hi"
	if toInbound(data).IsGroup() {
		t.Error("conversationType 1 must not be a group")
	}
}
