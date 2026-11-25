package chat

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type Image struct {
	MIMEType string
	Content  []byte
}

func (s *Service) SendImage(ctx context.Context, senderID, conversationID int64, clientID, caption string, image Image) (SendResult, error) {
	// 文件同步在会话事务之外进行，同群的文字发送无需等待图片写入。
	name := uuid.NewString() + "." + strings.TrimPrefix(image.MIMEType, "image/")
	if err := writeImageFile(s.imageDir, name, image.Content); err != nil {
		return SendResult{}, err
	}
	keepFile := false
	defer func() {
		if !keepFile {
			_ = os.Remove(filepath.Join(s.imageDir, name))
		}
	}()
	tx, err := s.store.BeginTx(ctx)
	if err != nil {
		return SendResult{}, err
	}
	defer tx.Rollback()
	// 复用普通消息的权限、重试查询和序号分配。
	result, err := s.SaveMessage(ctx, tx, senderID, conversationID, clientID, ImageMessage,
		func(_ Kind, _ []int64, message *Message) error {
			message.Content = caption
			message.ImagePath = sql.NullString{String: name, Valid: true}
			return nil
		})
	if err != nil {
		return SendResult{}, err
	}
	if !result.Created {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return SendResult{}, err
		}
		saved, err := os.ReadFile(filepath.Join(s.imageDir, result.Message.ImagePath.String))
		if err != nil {
			return SendResult{}, err
		}
		if !bytes.Equal(saved, image.Content) || result.Message.Content != caption {
			return SendResult{}, ErrMessageConflict
		}
		return result, nil
	}
	if err := recordMessageEvent(ctx, tx, result.Message.ID); err != nil {
		return SendResult{}, err
	}
	// 提交返回错误时，数据库可能已提交，因此保留文件。
	keepFile = true
	return result, s.commitMessage(tx, true)
}

func (s *Service) ReadImage(ctx context.Context, userID, messageID int64) (Image, error) {
	tx, err := s.store.BeginSnapshot(ctx)
	if err != nil {
		return Image{}, err
	}
	defer tx.Rollback()
	message, err := s.store.MessageByID(ctx, tx, messageID)
	if err != nil {
		return Image{}, err
	}
	member, err := s.RequireMember(ctx, tx, userID, message.ConversationID)
	if err != nil {
		return Image{}, err
	}
	if message.Seq <= member.JoinedSeq {
		return Image{}, ErrNotMember
	}
	if message.Kind != ImageMessage {
		return Image{}, sql.ErrNoRows
	}
	name := message.ImagePath.String
	content, err := os.ReadFile(filepath.Join(s.imageDir, name))
	return Image{MIMEType: mime.TypeByExtension(filepath.Ext(name)), Content: content}, err
}

func writeImageFile(dir, name string, content []byte) error {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".image-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, name))
}

// MigrateImageFiles 在启动时转存历史图片。全部文件保存且路径更新成功后才删除旧表。
func MigrateImageFiles(ctx context.Context, db *sqlx.DB, dir string) error {
	var exists bool
	if err := db.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name = 'message_images')`); err != nil || !exists {
		return err
	}
	var cursor int64
	for {
		var batch []struct {
			ID       int64  `db:"message_id"`
			MIMEType string `db:"mime_type"`
			Content  []byte `db:"content"`
		}
		if err := db.SelectContext(ctx, &batch, `SELECT message_id, mime_type, content FROM message_images
			WHERE message_id > ? ORDER BY message_id LIMIT 10`, cursor); err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			name := fmt.Sprintf("legacy-%d.%s", item.ID, strings.TrimPrefix(item.MIMEType, "image/"))
			if err := writeImageFile(dir, name, item.Content); err != nil {
				return err
			}
			if _, err := db.ExecContext(ctx, `UPDATE messages SET image_path = ? WHERE id = ? AND kind = 'image'`, name, item.ID); err != nil {
				return err
			}
			cursor = item.ID
		}
	}
	var missing int
	if err := db.GetContext(ctx, &missing, `SELECT COUNT(*) FROM messages WHERE kind = 'image' AND image_path IS NULL`); err != nil {
		return err
	}
	if missing != 0 {
		return fmt.Errorf("%d image messages have no image files", missing)
	}
	_, err := db.ExecContext(ctx, `DROP TABLE message_images`)
	return err
}
