package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/c926694/im/backend/internal/platform/mysql"
	"github.com/c926694/im/backend/internal/realtime"
	"github.com/c926694/im/backend/internal/user"
)

func TestAvatarMySQL(t *testing.T) {
	dsn := os.Getenv("IM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set IM_TEST_MYSQL_DSN to a migrated test database")
	}
	db, err := mysql.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := realtime.NewHub()
	server := testServer(t, NewRouter(user.NewStore(db), nil, nil, hub, testSecret, t.TempDir()), hub)
	account := registerAndLogin(t, server, "avatar", "")
	var profile user.Profile
	request(t, server, http.MethodGet, "/api/v1/me", account.Token, nil, http.StatusOK, &profile)
	if profile.AvatarURL != "/avatars/default.webp" {
		t.Fatalf("default avatar: %q", profile.AvatarURL)
	}
	var storedURL string
	if err := db.Get(&storedURL, `SELECT avatar_url FROM users WHERE id = ?`, account.ID); err != nil || storedURL != profile.AvatarURL {
		t.Fatalf("stored default avatar: %q, %v", storedURL, err)
	}

	upload := func(token string, data []byte) (int, string) {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("avatar", "avatar.webp")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPut, server.URL+"/api/v1/me/avatar", &body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var result struct {
			Code int
			Data struct {
				AvatarURL string `json:"avatar_url"`
			}
		}
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result.Code, result.Data.AvatarURL
	}
	if code, _ := upload("", defaultAvatar); code != codeInvalidToken {
		t.Fatalf("unauthenticated upload: %d", code)
	}
	for _, data := range [][]byte{[]byte("invalid image"), make([]byte, maxAvatarSize+1)} {
		if code, _ := upload(account.Token, data); code != codeInvalidRequest {
			t.Fatalf("invalid upload: %d", code)
		}
	}
	code, avatarURL := upload(account.Token, defaultAvatar)
	if code != 0 || !strings.HasPrefix(avatarURL, "/avatars/") || avatarURL == profile.AvatarURL {
		t.Fatalf("upload: %d %q", code, avatarURL)
	}
	if err := db.Get(&storedURL, `SELECT avatar_url FROM users WHERE id = ?`, account.ID); err != nil || storedURL != avatarURL {
		t.Fatalf("stored uploaded avatar: %q, %v", storedURL, err)
	}
	request(t, server, http.MethodGet, "/api/v1/me", account.Token, nil, http.StatusOK, &profile)
	if profile.AvatarURL != avatarURL {
		t.Fatalf("saved avatar: %q", profile.AvatarURL)
	}
	res, err := server.Client().Get(server.URL + avatarURL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK || !bytes.Equal(data, defaultAvatar) {
		t.Fatalf("served avatar: %d, %v", res.StatusCode, err)
	}
}
