package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/friendship"
	"github.com/c926694/im/backend/internal/platform/mysql"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/user"
)

func TestGroupChatMySQL(t *testing.T) {
	dsn := os.Getenv("IM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set IM_TEST_MYSQL_DSN to a migrated test database")
	}
	db, err := mysql.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	hub := realtime.NewHub()
	chats := chat.NewService(chat.NewStore(db))
	startNotifications(t, db, chats, hub)
	server := testServer(t, NewRouter(user.NewStore(db), chats, friendship.NewService(friendship.NewStore(db)), hub, testSecret, nil, nil), hub)
	owner := registerAndLogin(t, server, "owner", "")
	admin := registerAndLogin(t, server, "admin", "")
	member := registerAndLogin(t, server, "member", "")
	befriend(t, server, owner, admin)
	befriend(t, server, owner, member)

	const groups = "/api/v1/groups"
	var created struct{ ID string }
	request(t, server, http.MethodPost, groups, owner.Token, map[string]any{
		"name": "测试群聊", "member_ids": []string{admin.ID, member.ID, member.ID, owner.ID},
	}, http.StatusOK, &created)
	id, err := strconv.ParseInt(created.ID, 10, 64)
	if err != nil || id <= 0 {
		t.Fatalf("group ID = %q", created.ID)
	}
	group := groups + "/" + created.ID
	members := group + "/members"
	conversation := "/api/v1/conversations/" + created.ID
	messages := conversation + "/messages"
	conn, events := openDevice(t, server, admin.Token)
	first := send(t, server, messages, member.Token, 1)
	if first.Seq != 1 || first.SenderNickname != "member" {
		t.Fatalf("unexpected first group message: %+v", first)
	}
	assertPush(t, events, first)
	if retry := send(t, server, messages, member.Token, 1); retry != first {
		t.Fatalf("retry = %+v, want %+v", retry, first)
	}
	assertGroupError(t, server, http.MethodPatch, group, member.Token, map[string]string{"name": "无权修改"}, codeNotConversationUser)
	assertGroupError(t, server, http.MethodPost, group+"/leave", owner.Token, nil, codeResourceConflict)
	request(t, server, http.MethodPatch, members+"/"+admin.ID, owner.Token, map[string]string{"role": "admin"}, http.StatusOK, nil)
	assertGroupUpdate(t, events, id)
	request(t, server, http.MethodPost, members, owner.Token, map[string]any{"member_ids": []string{admin.ID}}, http.StatusOK, nil)
	assertGroupUpdate(t, events, id)
	var details groupResponse
	request(t, server, http.MethodGet, group, admin.Token, nil, http.StatusOK, &details)
	roles := make(map[string]chat.Role, len(details.Members))
	for _, entry := range details.Members {
		roles[strconv.FormatInt(entry.ID, 10)] = entry.Role
	}
	if len(details.Members) != 3 || roles[owner.ID] != chat.Owner || roles[admin.ID] != chat.Admin || roles[member.ID] != chat.Normal {
		t.Fatalf("unexpected group roles: %v", roles)
	}
	assertReadProgress(t, server, admin.Token, 0, 1)
	assertReadProgress(t, server, member.Token, 0, 0)

	// 断线期间发送的消息通过 HTTP 增量查询补齐。
	conn.Close()
	second := send(t, server, messages, owner.Token, 2)
	waitMessagePublished(t, db, second.ID)
	_, events = openDevice(t, server, admin.Token)
	var history []messageResponse
	request(t, server, http.MethodGet, messages+"?after_seq=1", admin.Token, nil, http.StatusOK, &history)
	if len(history) != 1 || history[0] != second {
		t.Fatalf("sync history = %+v, want %+v", history, second)
	}
	_, memberEvents := openDevice(t, server, member.Token)
	assertGroupError(t, server, http.MethodDelete, members+"/"+owner.ID, admin.Token, nil, codeNotConversationUser)
	request(t, server, http.MethodDelete, members+"/"+member.ID, admin.Token, nil, http.StatusOK, nil)
	assertGroupUpdate(t, events, id)
	assertGroupUpdate(t, memberEvents, id)
	assertGroupError(t, server, http.MethodGet, messages+"?after_seq=0", member.Token, nil, codeNotConversationUser)
	assertGroupError(t, server, http.MethodGet, messages, member.Token, nil, codeNotConversationUser)
	assertGroupError(t, server, http.MethodPost, conversation+"/read", member.Token, map[string]string{"seq": "1"}, codeNotConversationUser)
	assertGroupError(t, server, http.MethodPost, messages, member.Token,
		map[string]string{"client_msg_id": first.ClientMsgID, "content": first.Content}, codeNotConversationUser)
	third := send(t, server, messages, admin.Token, 3)
	assertPush(t, events, third)
	// 被移除的账号仍保持连接，下一条收到的事件应是重新加入通知。
	assertGroupError(t, server, http.MethodPost, members, admin.Token, map[string]any{"member_ids": []string{member.ID}}, codeFriendshipRequired)
	request(t, server, http.MethodPost, members, owner.Token, map[string]any{"member_ids": []string{member.ID}}, http.StatusOK, nil)
	assertGroupUpdate(t, events, id)
	assertGroupUpdate(t, memberEvents, id)
	var summaries []conversationResponse
	request(t, server, http.MethodGet, "/api/v1/conversations", member.Token, nil, http.StatusOK, &summaries)
	if len(summaries) != 1 || summaries[0].JoinedSeq != 3 || summaries[0].LastReadSeq != 3 ||
		summaries[0].UnreadCount != 0 || summaries[0].LastMessage != "" || summaries[0].Kind != chat.Group {
		t.Fatalf("unexpected summary after rejoining: %+v", summaries)
	}
	assertGroupError(t, server, http.MethodPost, messages, member.Token,
		map[string]string{"client_msg_id": first.ClientMsgID, "content": first.Content}, codeResourceConflict)
	fourth := send(t, server, messages, admin.Token, 4)
	assertPush(t, events, fourth)
	assertPush(t, memberEvents, fourth)
	for _, query := range []string{"", "?after_seq=0"} {
		request(t, server, http.MethodGet, messages+query, member.Token, nil, http.StatusOK, &history)
		if len(history) != 1 || history[0] != fourth || fourth.Seq != 4 {
			t.Fatalf("history after rejoining (%q) = %+v", query, history)
		}
	}
	assertReadProgress(t, server, member.Token, 3, 1)
	request(t, server, http.MethodPost, conversation+"/read", member.Token, map[string]string{"seq": "4"}, http.StatusOK, nil)
	assertReadProgress(t, server, member.Token, 4, 0)

	request(t, server, http.MethodPost, group+"/transfer", owner.Token, map[string]string{"user_id": admin.ID}, http.StatusOK, nil)
	assertGroupError(t, server, http.MethodPatch, group, owner.Token, map[string]string{"name": "无权修改"}, codeNotConversationUser)
	request(t, server, http.MethodPatch, group, admin.Token, map[string]string{"name": "已转让群聊"}, http.StatusOK, nil)
	request(t, server, http.MethodGet, group, admin.Token, nil, http.StatusOK, &details)
	if details.Name != "已转让群聊" {
		t.Fatalf("group name = %q", details.Name)
	}
	for _, account := range []testAccount{owner, member, admin} {
		request(t, server, http.MethodPost, group+"/leave", account.Token, nil, http.StatusOK, nil)
	}
	assertGroupError(t, server, http.MethodGet, group, admin.Token, nil, codeResourceNotFound)
	request(t, server, http.MethodGet, "/api/v1/conversations", admin.Token, nil, http.StatusOK, &summaries)
	if len(summaries) != 0 {
		t.Fatalf("closed group remains in conversation list: %+v", summaries)
	}
}

func assertGroupError(t *testing.T, server *httptest.Server, method, path, token string, body any, code int) {
	t.Helper()
	var envelope apiResponse
	request(t, server, method, path, token, body, http.StatusBadRequest, &envelope)
	if envelope.Code != code || string(envelope.Data) != "null" {
		t.Fatalf("%s %s: response = %+v, want code %d and null data", method, path, envelope, code)
	}
}

func assertGroupUpdate(t *testing.T, events <-chan messageEvent, id int64) {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != "conversation.updated" || event.Data.ConversationID != id {
			t.Fatalf("event = %+v, want group update for %d", event, id)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no update for group %d", id)
	}
}
