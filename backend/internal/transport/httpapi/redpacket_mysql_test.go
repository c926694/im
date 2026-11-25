package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/platform/mysql"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/social"
	"github.com/c926694/im/backend/internal/user"
)

type sentPacket struct {
	ID      string          `json:"red_packet_id"`
	Message messageResponse `json:"message"`
}

func TestRedPacketsMySQL(t *testing.T) {
	dsn := os.Getenv("IM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set IM_TEST_MYSQL_DSN to a migrated test database")
	}
	db, err := mysql.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	chats, packets := newTestServices(db, t.TempDir())
	hub := realtime.NewHub()
	server := testServer(t, NewRouter(user.NewStore(db), chats, packets, hub, testSecret, t.TempDir()), hub)
	alice := registerAndLogin(t, server, "packet_alice", "")
	bob := registerAndLogin(t, server, "packet_bob", "")
	carol := registerAndLogin(t, server, "packet_carol", "")
	befriend(t, server, alice, bob)
	befriend(t, server, alice, carol)
	assertPoints(t, server, alice, "1000")
	var ledger []user.PointEntry
	request(t, server, http.MethodGet, "/api/v1/points/ledger", alice.Token, nil, http.StatusOK, &ledger)
	if len(ledger) != 1 || ledger[0].Kind != user.PointGrant || ledger[0].Delta != 1000 || ledger[0].BalanceAfter != 1000 {
		t.Fatalf("initial ledger = %+v", ledger)
	}

	var direct, group struct{ ID string }
	request(t, server, http.MethodPost, "/api/v1/conversations/direct", alice.Token,
		map[string]string{"peer_username": bob.Username}, http.StatusOK, &direct)
	request(t, server, http.MethodPost, "/api/v1/groups", alice.Token,
		map[string]any{"name": "积分测试群", "member_ids": []string{bob.ID, carol.ID}}, http.StatusOK, &group)
	path := "/api/v1/conversations/" + direct.ID + "/red-packets"
	assertGroupError(t, server, http.MethodPost, path, alice.Token, packetInput(1, "1001", 1), codeInsufficientPoints)
	var history []messageResponse
	request(t, server, http.MethodGet, "/api/v1/conversations/"+direct.ID+"/messages", alice.Token, nil, http.StatusOK, &history)
	if len(history) != 0 {
		t.Fatalf("failed send created messages: %+v", history)
	}
	assertPoints(t, server, alice, "1000")

	var single, retry sentPacket
	request(t, server, http.MethodPost, path, alice.Token, packetInput(1, "100", 1), http.StatusOK, &single)
	request(t, server, http.MethodPost, path, alice.Token, packetInput(1, "100", 1), http.StatusOK, &retry)
	if retry.ID != single.ID || retry.Message.ID != single.Message.ID || single.Message.Kind != chat.RedPacketMessage || single.Message.Seq != 1 ||
		single.Message.RedPacketID == nil || strconv.FormatInt(*single.Message.RedPacketID, 10) != single.ID {
		t.Fatalf("send = %+v, retry = %+v", single, retry)
	}
	assertGroupError(t, server, http.MethodPost, path, alice.Token, packetInput(1, "101", 1), codeResourceConflict)
	assertGroupError(t, server, http.MethodPost, "/api/v1/conversations/"+direct.ID+"/messages", alice.Token,
		map[string]any{"client_msg_id": packetInput(1, "100", 1)["client_msg_id"], "content": "text"}, codeResourceConflict)
	assertPoints(t, server, alice, "900")

	singlePath := "/api/v1/red-packets/" + single.ID
	assertGroupError(t, server, http.MethodGet, singlePath, carol.Token, nil, codeNotConversationUser)
	assertGroupError(t, server, http.MethodPost, singlePath+"/claim", alice.Token, nil, codeNotConversationUser)
	request(t, server, http.MethodDelete, "/api/v1/friends/"+bob.ID, alice.Token, nil, http.StatusOK, nil)
	assertGroupError(t, server, http.MethodPost, path, alice.Token, packetInput(4, "100", 1), codeFriendshipRequired)
	var firstClaim, repeatClaim social.Claim
	request(t, server, http.MethodPost, singlePath+"/claim", bob.Token, nil, http.StatusOK, &firstClaim)
	request(t, server, http.MethodPost, singlePath+"/claim", bob.Token, nil, http.StatusOK, &repeatClaim)
	if firstClaim.Points != 100 || firstClaim != repeatClaim {
		t.Fatalf("claim = %+v, retry = %+v", firstClaim, repeatClaim)
	}
	request(t, server, http.MethodGet, "/api/v1/conversations/"+direct.ID+"/messages", alice.Token, nil, http.StatusOK, &history)
	claimantID, _ := strconv.ParseInt(bob.ID, 10, 64)
	if len(history) != 2 || history[0].Kind != chat.RedPacketClaimMessage || history[0].SenderID != claimantID ||
		history[0].Content != "领取了红包" || history[1].Kind != chat.RedPacketMessage || history[1].RedPacketStatus != string(social.Claimed) {
		t.Fatalf("claimed packet history = %+v", history)
	}
	var claimNoticeCount int
	if err := db.Get(&claimNoticeCount, `SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND sender_id = ? AND kind = 'red_packet_claim'`, direct.ID, bob.ID); err != nil || claimNoticeCount != 1 {
		t.Fatalf("claim notice count = %d: %v", claimNoticeCount, err)
	}
	assertPoints(t, server, bob, "1100")

	groupSendPath := "/api/v1/conversations/" + group.ID + "/red-packets"
	assertGroupError(t, server, http.MethodPost, groupSendPath, alice.Token, packetInput(2, "25", 3), codeInvalidRequest)
	var shared sentPacket
	request(t, server, http.MethodPost, groupSendPath, alice.Token, packetInput(2, "25", 1), http.StatusOK, &shared)
	sharedID, _ := strconv.ParseInt(shared.ID, 10, 64)
	bobID, _ := strconv.ParseInt(bob.ID, 10, 64)
	carolID, _ := strconv.ParseInt(carol.ID, 10, 64)
	outcomes := make(chan error, 3)
	var start sync.WaitGroup
	start.Add(1)
	for _, id := range []int64{bobID, bobID, carolID} {
		go func() {
			start.Wait()
			_, err := packets.Claim(context.Background(), sharedID, id)
			outcomes <- err
		}()
	}
	start.Done()
	success := 0
	for range 3 {
		switch err := <-outcomes; err {
		case nil:
			success++
		case social.ErrClaimed:
		default:
			t.Fatalf("concurrent claim: %v", err)
		}
	}
	if success < 1 || success > 2 {
		t.Fatalf("unexpected claim outcomes: %d", success)
	}
	var claimCount int
	if err := db.Get(&claimCount, `SELECT COUNT(*) FROM red_packet_claims WHERE red_packet_id = ?`, sharedID); err != nil || claimCount != 1 {
		t.Fatalf("last share paid %d times: %v", claimCount, err)
	}

	var expiring sentPacket
	request(t, server, http.MethodPost, groupSendPath, alice.Token, packetInput(3, "30", 2), http.StatusOK, &expiring)
	expiringPath := "/api/v1/red-packets/" + expiring.ID
	request(t, server, http.MethodPost, expiringPath+"/claim", bob.Token, nil, http.StatusOK, nil)
	expiringID, _ := strconv.ParseInt(expiring.ID, 10, 64)
	if err := packets.RefundDue(context.Background(), expiringID); err != social.ErrNotYetExpired {
		t.Fatalf("refund before expiry: %v", err)
	}
	if _, err := db.Exec(`UPDATE red_packets SET expires_at = UTC_TIMESTAMP(6) WHERE id = ?`, expiring.ID); err != nil {
		t.Fatal(err)
	}

	var concurrent sync.WaitGroup
	concurrent.Add(3)
	failures := make(chan error, 3)
	for range 2 {
		go func() { defer concurrent.Done(); failures <- packets.RefundDue(context.Background(), expiringID) }()
	}
	go func() {
		defer concurrent.Done()
		id, _ := strconv.ParseInt(expiring.ID, 10, 64)
		_, err := packets.Claim(context.Background(), id, carolID)
		if err == social.ErrExpired {
			err = nil
		} else {
			err = fmt.Errorf("claim past expiry: %v", err)
		}
		failures <- err
	}()
	concurrent.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertGroupError(t, server, http.MethodPost, expiringPath+"/claim", carol.Token, nil, codeRedPacketExpired)
	var money struct {
		Sent     int64 `db:"sent"`
		Claimed  int64 `db:"claimed"`
		Refunded int64 `db:"refunded"`
		Refunds  int   `db:"refunds"`
	}
	if err := db.Get(&money, `SELECT SUM(IF(kind='send', -delta, 0)) AS sent, SUM(IF(kind='claim', delta, 0)) AS claimed,
		SUM(IF(kind='refund', delta, 0)) AS refunded, SUM(kind='refund') AS refunds FROM point_ledger
		WHERE kind IN ('send','claim','refund') AND ref_id = ?`, expiring.ID); err != nil {
		t.Fatal(err)
	}
	if money.Sent != 60 || money.Claimed != 30 || money.Refunded != 30 || money.Refunds != 1 {
		t.Fatalf("packet reconciliation: %+v", money)
	}
	assertPoints(t, server, alice, "845")

	groupPath := "/api/v1/groups/" + group.ID
	request(t, server, http.MethodDelete, groupPath+"/members/"+carol.ID, alice.Token, nil, http.StatusOK, nil)
	assertGroupError(t, server, http.MethodGet, expiringPath, carol.Token, nil, codeNotConversationUser)
	request(t, server, http.MethodPost, groupPath+"/members", alice.Token, map[string]any{"member_ids": []string{carol.ID}}, http.StatusOK, nil)
	assertGroupError(t, server, http.MethodPost, expiringPath+"/claim", carol.Token, nil, codeNotConversationUser)
	var eventCount int
	if err := db.Get(&eventCount, `SELECT COUNT(*) FROM outbox_events WHERE red_packet_id = ?`, expiring.ID); err != nil || eventCount != 4 {
		t.Fatalf("red packet outbox events = %d: %v", eventCount, err)
	}
	request(t, server, http.MethodGet, "/api/v1/points/ledger?limit=1", alice.Token, nil, http.StatusOK, &ledger)
	if len(ledger) != 1 || ledger[0].Kind != user.PointRefund || ledger[0].BalanceAfter != 845 {
		t.Fatalf("last ledger page: %+v", ledger)
	}
	request(t, server, http.MethodGet, "/api/v1/points/ledger?limit=1&before_id="+strconv.FormatInt(ledger[0].ID, 10), alice.Token, nil, http.StatusOK, &ledger)
	if len(ledger) != 1 || ledger[0].Kind != user.PointSend || ledger[0].BalanceAfter != 815 {
		t.Fatalf("earlier ledger page: %+v", ledger)
	}
}

func packetInput(key int, points string, count int) map[string]any {
	return map[string]any{"client_msg_id": fmt.Sprintf("7a4ac694-2a74-4aa6-9611-%012x", key), "points_per_share": points, "share_count": count}
}

func assertPoints(t *testing.T, server *httptest.Server, account testAccount, balance string) {
	t.Helper()
	var data struct{ Balance string }
	request(t, server, http.MethodGet, "/api/v1/points/balance", account.Token, nil, http.StatusOK, &data)
	if data.Balance != balance {
		t.Fatalf("balance = %s, want %s", data.Balance, balance)
	}
}
