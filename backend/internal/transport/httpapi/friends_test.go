package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/friendship"
	"github.com/c926694/im/backend/internal/platform/mysql"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/user"
)

func TestFriendRequestsMySQL(t *testing.T) {
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
	server := testServer(t, NewRouter(user.NewStore(db), chat.NewService(chat.NewStore(db)), friendship.NewService(friendship.NewStore(db)), hub, testSecret), hub)
	alice := registerAndLogin(t, server, "alice", "alice-password")
	bob := registerAndLogin(t, server, "bob", "bob-password")
	out := registerAndLogin(t, server, "outsider", "other-password")

	const path = "/api/v1/friend-requests"
	const friendsPath = "/api/v1/friends"
	lookup := "/api/v1/users/by-username?username="
	var profile user.Profile
	request(t, server, http.MethodGet, lookup+bob.Username, "", nil, http.StatusUnauthorized, nil)
	request(t, server, http.MethodGet, lookup+url.QueryEscape(" "+strings.ToUpper(bob.Username)+" "), alice.Token, nil, http.StatusOK, &profile)
	if profile.Username != bob.Username || profile.Nickname != "bob" {
		t.Fatalf("lookup returned %+v", profile)
	}
	request(t, server, http.MethodGet, lookup+bob.Username+"_missing", alice.Token, nil, http.StatusNotFound, nil)
	request(t, server, http.MethodPost, path, alice.Token, map[string]string{"peer_username": alice.Username}, http.StatusBadRequest, nil)

	var first, duplicate, next friendRequestResponse
	apply := map[string]string{"peer_username": bob.Username}
	accept := map[string]string{"status": "accepted"}
	reject := map[string]string{"status": "rejected"}
	request(t, server, http.MethodPost, path, alice.Token, apply, http.StatusOK, &first)
	request(t, server, http.MethodPost, path, alice.Token, apply, http.StatusOK, &duplicate)
	if first.ID == "" || duplicate != first || first.Peer != profile || first.Direction != "outgoing" {
		t.Fatalf("first request = %+v, duplicate = %+v", first, duplicate)
	}
	var conflict apiResponse
	request(t, server, http.MethodPost, path, bob.Token, map[string]string{"peer_username": alice.Username}, http.StatusConflict, &conflict)
	if conflict.Code != codeResourceConflict || conflict.Msg == "" {
		t.Fatalf("incoming request conflict = %+v", conflict)
	}
	var list []friendRequestResponse
	request(t, server, http.MethodGet, path, bob.Token, nil, http.StatusOK, &list)
	if len(list) != 1 || list[0].ID != first.ID || list[0].Direction != "incoming" || list[0].Peer.Username != alice.Username {
		t.Fatalf("Bob's requests = %+v", list)
	}
	request(t, server, http.MethodGet, path, alice.Token, nil, http.StatusOK, &list)
	if len(list) != 1 || list[0] != first {
		t.Fatalf("Alice's requests = %+v", list)
	}
	request(t, server, http.MethodGet, path, out.Token, nil, http.StatusOK, &list)
	if len(list) != 0 {
		t.Fatalf("outsider's requests = %+v", list)
	}

	directPath := "/api/v1/conversations/direct"
	request(t, server, http.MethodPost, directPath, alice.Token, apply, http.StatusForbidden, nil)
	request(t, server, http.MethodPatch, path+"/"+first.ID, alice.Token, accept, http.StatusNotFound, nil)
	request(t, server, http.MethodPatch, path+"/"+first.ID, out.Token, accept, http.StatusNotFound, nil)
	request(t, server, http.MethodPatch, path+"/"+first.ID, bob.Token, reject, http.StatusOK, nil)
	request(t, server, http.MethodPost, directPath, alice.Token, apply, http.StatusForbidden, nil)
	request(t, server, http.MethodPost, path, alice.Token, apply, http.StatusOK, &next)
	if next.ID == first.ID {
		t.Fatal("reapplied with the old request ID")
	}
	request(t, server, http.MethodPatch, path+"/"+first.ID, bob.Token, accept, http.StatusNotFound, nil)
	request(t, server, http.MethodPatch, path+"/"+next.ID, bob.Token, accept, http.StatusOK, nil)
	request(t, server, http.MethodPatch, path+"/"+next.ID, bob.Token, reject, http.StatusNotFound, nil)
	request(t, server, http.MethodPost, path, alice.Token, apply, http.StatusConflict, nil)
	request(t, server, http.MethodGet, path, bob.Token, nil, http.StatusOK, &list)
	if len(list) != 0 {
		t.Fatalf("resolved requests remain pending: %+v", list)
	}

	for i, pair := range [][2]testAccount{{alice, bob}, {bob, alice}, {alice, bob}, {bob, alice}} {
		if i == 2 {
			request(t, server, http.MethodDelete, friendsPath+"/"+alice.ID, bob.Token, nil, http.StatusOK, nil)
		}
		var profiles []user.Profile
		request(t, server, http.MethodGet, friendsPath, pair[0].Token, nil, http.StatusOK, &profiles)
		if i < 2 && (len(profiles) != 1 || profiles[0].Username != pair[1].Username) || i >= 2 && len(profiles) != 0 {
			t.Fatalf("%s's friends = %+v", pair[0].Username, profiles)
		}
	}
}
