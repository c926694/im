package chat_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/c926694/im/backend/internal/chat"
	"github.com/c926694/im/backend/internal/platform/mysql"
)

func TestStoreMySQL(t *testing.T) {
	dsn := os.Getenv("IM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set IM_TEST_MYSQL_DSN to a migrated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := mysql.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	alice, bob, outsider := insertUser(t, ctx, db), insertUser(t, ctx, db), insertUser(t, ctx, db)
	store := chat.NewStore(db)
	if _, err := store.CreateDirect(ctx, alice, 1<<62); !errors.Is(err, chat.ErrUserNotFound) {
		t.Fatalf("open with unknown user error = %v", err)
	}

	var conversationIDs [8]int64
	runConcurrently(t, len(conversationIDs), func(i int) error {
		userID, peerID := alice, bob
		if i%2 != 0 {
			userID, peerID = bob, alice
		}
		id, err := store.CreateDirect(ctx, userID, peerID)
		conversationIDs[i] = id
		return err
	})
	conversationID := conversationIDs[0]
	if conversationID == 0 {
		t.Fatal("missing conversation ID")
	}
	for _, id := range conversationIDs {
		if id != conversationID {
			t.Fatalf("created different conversations: %v", conversationIDs)
		}
	}
	var members int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_members WHERE conversation_id = ?`, conversationID).Scan(&members); err != nil || members != 2 {
		t.Fatalf("conversation has %d members, want 2 (err = %v)", members, err)
	}
	empty, err := store.ListConversations(ctx, bob)
	if err != nil || len(empty) != 1 || empty[0].LastSeq != 0 || empty[0].LastReadSeq != 0 || empty[0].UnreadCount != 0 {
		t.Fatalf("unexpected empty conversation: %+v, err = %v", empty, err)
	}
	const clientMsgID = "7a4ac694-2a74-4aa6-9611-503ffafc2b15"
	draft := messageDraft(t, strings.ToUpper(clientMsgID), " 你好 👋 ")
	if _, err := store.SendMessage(ctx, outsider, conversationID, draft); !errors.Is(err, chat.ErrNotMember) {
		t.Fatalf("outsider send error = %v", err)
	}
	if _, err := store.History(ctx, outsider, conversationID, 0, 0); !errors.Is(err, chat.ErrNotMember) {
		t.Fatalf("outsider history error = %v", err)
	}
	if _, err := store.Sync(ctx, outsider, conversationID, 0, 0); !errors.Is(err, chat.ErrNotMember) {
		t.Fatalf("outsider sync error = %v", err)
	}
	if err := store.MarkRead(ctx, outsider, conversationID, 0); !errors.Is(err, chat.ErrNotMember) {
		t.Fatalf("outsider read error = %v", err)
	}

	var retries [8]chat.SendResult
	runConcurrently(t, len(retries), func(i int) error {
		message, err := store.SendMessage(ctx, alice, conversationID, draft)
		retries[i] = message
		return err
	})
	first := retries[0].Message
	if first.ID == 0 || first.Seq != 1 || first.ClientMsgID != clientMsgID || first.Content != " 你好 👋 " || first.CreatedAt.Location() != time.UTC {
		t.Fatalf("unexpected first message: %+v", first)
	}
	created := 0
	for _, retry := range retries {
		if retry.Message != first || retry.RecipientID != bob {
			t.Fatalf("retry returned %+v, want %+v", retry, first)
		}
		if retry.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d messages, want 1", created)
	}
	var drafts [8]chat.MessageDraft
	for i := range drafts {
		drafts[i] = messageDraft(t, fmt.Sprintf("7a4ac694-2a74-4aa6-9611-%012x", i), "concurrent message")
	}
	runConcurrently(t, len(drafts), func(i int) error {
		_, err := store.SendMessage(ctx, alice, conversationID, drafts[i])
		return err
	})
	// The same client ID belongs to a different request when the sender differs.
	reply, err := store.SendMessage(ctx, bob, conversationID, messageDraft(t, clientMsgID, "reply"))
	if err != nil || reply.Message.Seq != 10 || reply.RecipientID != alice || !reply.Created {
		t.Fatalf("reply = %+v, err = %v; want seq 10", reply, err)
	}

	latest, err := store.History(ctx, bob, conversationID, 0, 2)
	assertSequences(t, latest, err, 10, 9)
	older, err := store.History(ctx, bob, conversationID, latest[1].Seq, 0)
	assertSequences(t, older, err, 8, 7, 6, 5, 4, 3, 2, 1)
	missed, err := store.Sync(ctx, bob, conversationID, 6, 2)
	assertSequences(t, missed, err, 7, 8)
	next, err := store.Sync(ctx, bob, conversationID, missed[1].Seq, 2)
	assertSequences(t, next, err, 9, 10)

	if err := store.MarkRead(ctx, bob, conversationID, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRead(ctx, bob, conversationID, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRead(ctx, bob, conversationID, 11); !errors.Is(err, chat.ErrInvalidReadSeq) {
		t.Fatalf("future read sequence error = %v", err)
	}
	if reopened, err := store.CreateDirect(ctx, bob, alice); err != nil || reopened != conversationID {
		t.Fatalf("reopened conversation = %d, err = %v", reopened, err)
	}
	bobs, err := store.ListConversations(ctx, bob)
	if err != nil || len(bobs) != 1 {
		t.Fatalf("Bob's conversations = %+v, err = %v", bobs, err)
	}
	if item := bobs[0]; item.ID != conversationID || item.PeerUserID != alice || item.LastReadSeq != 2 || item.LastSeq != 10 || item.UnreadCount != 7 || !item.LastActivityAt.Equal(reply.Message.CreatedAt) {
		t.Fatalf("unexpected Bob summary: %+v", item)
	}
	alices, err := store.ListConversations(ctx, alice)
	if err != nil || len(alices) != 1 || alices[0].UnreadCount != 1 || alices[0].LastReadSeq != 0 {
		t.Fatalf("Alice should only have Bob's reply unread: %+v, err = %v", alices, err)
	}

	otherID, err := store.CreateDirect(ctx, bob, outsider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SendMessage(ctx, outsider, otherID, draft); err != nil {
		t.Fatal(err)
	}
	bobs, err = store.ListConversations(ctx, bob)
	if err != nil || len(bobs) != 2 || bobs[0].ID != otherID || bobs[0].UnreadCount != 1 || bobs[1].ID != conversationID || bobs[1].UnreadCount != 7 {
		t.Fatalf("Bob's conversations with unread counts: %+v, err = %v", bobs, err)
	}
}

func insertUser(t *testing.T, ctx context.Context, db *sqlx.DB) int64 {
	t.Helper()
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, nickname) VALUES (?, ?, ?)`,
		fmt.Sprintf("test_%x", nonce), "fixture", "integration user")
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func messageDraft(t *testing.T, clientID, content string) chat.MessageDraft {
	t.Helper()
	draft, err := chat.NewMessageDraft(clientID, content)
	if err != nil {
		t.Fatal(err)
	}
	return draft
}

func runConcurrently(t *testing.T, n int, run func(int) error) {
	t.Helper()
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = run(i)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent request %d: %v", i, err)
		}
	}
}

func assertSequences(t *testing.T, messages []chat.Message, err error, want ...int64) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int64, 0, len(messages))
	for _, message := range messages {
		got = append(got, message.Seq)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sequences = %v, want %v", got, want)
	}
}
