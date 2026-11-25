package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/platform/mysql"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/user"
	"github.com/google/uuid"
)

func TestChatImagesMySQL(t *testing.T) {
	dsn := os.Getenv("IM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set IM_TEST_MYSQL_DSN to a migrated test database")
	}
	db, err := mysql.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	imageDir := t.TempDir()
	hub := realtime.NewHub()
	chats, socialService := newTestServices(db, imageDir)
	startNotifications(t, db, chats, socialService, hub)
	server := testServer(t, NewRouter(user.NewStore(db), chats, socialService, hub, testSecret, t.TempDir()), hub)
	alice := registerAndLogin(t, server, "image_alice", "")
	bob := registerAndLogin(t, server, "image_bob", "")
	carol := registerAndLogin(t, server, "image_carol", "")
	befriend(t, server, alice, bob)
	var direct, group struct{ ID string }
	request(t, server, http.MethodPost, "/api/v1/conversations/direct", alice.Token,
		map[string]string{"peer_username": bob.Username}, http.StatusOK, &direct)
	request(t, server, http.MethodPost, "/api/v1/groups", alice.Token,
		map[string]any{"name": "图片测试群", "member_ids": []string{bob.ID}}, http.StatusOK, &group)

	upload := func(conversation, token, clientID string, data []byte, wantCode int, captions ...string) messageResponse {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("image", "chat.webp")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteField("client_msg_id", clientID); err != nil {
			t.Fatal(err)
		}
		if len(captions) > 0 {
			if err := writer.WriteField("content", captions[0]); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/conversations/"+conversation+"/messages", &body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var result struct {
			Code int
			Data messageResponse
		}
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.Code != wantCode {
			t.Fatalf("upload code = %d, want %d", result.Code, wantCode)
		}
		return result.Data
	}
	read := func(token string, message messageResponse) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/messages/"+strconv.FormatInt(message.ID, 10)+"/image", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil || res.Header.Get("Content-Type") != "image/webp" || !bytes.Equal(data, defaultAvatar) {
			t.Fatalf("unexpected image response: %v", err)
		}
	}
	id := uuid.NewString()
	_, events := openDevice(t, server, bob.Token)
	upload(direct.ID, carol.Token, id, defaultAvatar, codeNotConversationUser)
	upload(direct.ID, alice.Token, id, []byte("invalid"), codeInvalidRequest)
	upload(direct.ID, alice.Token, id, make([]byte, maxChatImageSize+1), codeInvalidRequest)
	message := upload(direct.ID, alice.Token, id, defaultAvatar, 0, "截图说明")
	assertPush(t, events, message)
	retry := upload(direct.ID, alice.Token, id, defaultAvatar, 0, "截图说明")
	upload(direct.ID, alice.Token, id, defaultAvatar, codeResourceConflict, "修改说明")
	if message.Kind != chat.ImageMessage || message.Content != "截图说明" || message.ID != retry.ID || message.Seq != 1 {
		t.Fatalf("send = %+v; retry = %+v", message, retry)
	}
	var another bytes.Buffer
	if err := png.Encode(&another, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	upload(direct.ID, alice.Token, id, another.Bytes(), codeResourceConflict, "截图说明")
	imagePath := "/api/v1/messages/" + strconv.FormatInt(message.ID, 10) + "/image"
	assertGroupError(t, server, http.MethodGet, imagePath, carol.Token, nil, codeNotConversationUser)
	var sync []messageResponse
	request(t, server, http.MethodGet, "/api/v1/conversations/"+direct.ID+"/messages?after_seq=0", bob.Token, nil, http.StatusOK, &sync)
	if len(sync) != 1 || sync[0].ID != message.ID || sync[0].Content != "截图说明" {
		t.Fatalf("sync: %+v", sync)
	}
	groupImage := upload(group.ID, alice.Token, uuid.NewString(), defaultAvatar, 0)
	read(bob.Token, groupImage)
	members := "/api/v1/groups/" + group.ID + "/members"
	request(t, server, http.MethodDelete, members+"/"+bob.ID, alice.Token, nil, http.StatusOK, nil)
	request(t, server, http.MethodPost, members, alice.Token, map[string]any{"member_ids": []string{bob.ID}}, http.StatusOK, nil)
	assertGroupError(t, server, http.MethodGet, "/api/v1/messages/"+strconv.FormatInt(groupImage.ID, 10)+"/image", bob.Token, nil, codeNotConversationUser)
	request(t, server, http.MethodDelete, "/api/v1/friends/"+bob.ID, alice.Token, nil, http.StatusOK, nil)
	read(bob.Token, message)
	upload(direct.ID, alice.Token, uuid.NewString(), defaultAvatar, codeFriendshipRequired)

	// 使用旧表结构保存一条历史图片，验证升级后通过原消息 ID 读取相同内容。
	var hasLegacyTable bool
	if err := db.Get(&hasLegacyTable, `SELECT EXISTS(SELECT 1 FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name = 'message_images')`); err != nil {
		t.Fatal(err)
	}
	if !hasLegacyTable {
		return
	}
	digest := sha256.Sum256(defaultAvatar)
	if _, err := db.Exec(`INSERT INTO message_images (message_id, mime_type, content, digest) VALUES (?, ?, ?, ?)`,
		message.ID, "image/webp", defaultAvatar, digest[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE messages SET image_path = NULL WHERE id = ?`, message.ID); err != nil {
		t.Fatal(err)
	}
	if err := chat.MigrateImageFiles(context.Background(), db, imageDir); err != nil {
		t.Fatal(err)
	}
	if err := chat.MigrateImageFiles(context.Background(), db, imageDir); err != nil {
		t.Fatal(err)
	}
	read(bob.Token, message)
}
