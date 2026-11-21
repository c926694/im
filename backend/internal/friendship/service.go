package friendship

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

var (
	ErrSelfRequest        = errors.New("cannot send a friend request to yourself")
	ErrIncomingRequest    = errors.New("the other user has already sent a friend request")
	ErrAlreadyFriends     = errors.New("users are already friends")
	ErrRequestUnavailable = errors.New("friend request cannot be processed by this user")
	ErrNotFriends         = errors.New("users are not friends")
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) Request(ctx context.Context, requesterID, peerID int64) (Relationship, error) {
	if requesterID == peerID {
		return Relationship{}, ErrSelfRequest
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Relationship{}, err
	}
	next := Relationship{
		PeerID: peerID, RequestID: hex.EncodeToString(nonce[:]), RequestedBy: requesterID,
		Status: Pending, RequestedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	low, high := min(requesterID, peerID), max(requesterID, peerID)
	tx, err := s.store.db.BeginTxx(ctx, nil)
	if err != nil {
		return Relationship{}, err
	}
	defer tx.Rollback()

	// 首次申请创建关系，并发申请由数据库的用户对主键协调。
	if err := s.store.insertRequestIfAbsent(ctx, tx, low, high, next); err != nil {
		return Relationship{}, err
	}
	// 根据当前关系状态决定沿用原申请、创建新申请或拒绝操作。
	current, err := s.store.findRelationship(ctx, tx, requesterID, low, high)
	if err != nil {
		return Relationship{}, err
	}
	switch current.Status {
	case Accepted:
		return Relationship{}, ErrAlreadyFriends
	case Pending:
		if current.RequestedBy != requesterID {
			return Relationship{}, ErrIncomingRequest
		}
	case Rejected, Removed:
		// 新申请更换标识，旧页面上的操作不能处理它。
		if err := s.store.replaceRequest(ctx, tx, low, high, next); err != nil {
			return Relationship{}, err
		}
		current = next
	}
	if err := tx.Commit(); err != nil {
		return Relationship{}, err
	}
	return current, nil
}

func (s *Service) Resolve(ctx context.Context, recipientID int64, requestID string, status Status) error {
	// 在更新时同时匹配接收人和待处理状态，避免并发操作重复处理同一份申请。
	count, err := s.store.resolvePending(ctx, recipientID, requestID, status)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrRequestUnavailable
	}
	return nil
}

func (s *Service) Remove(ctx context.Context, userID, peerID int64) error {
	return s.store.removeAccepted(ctx, userID, peerID)
}

func (s *Service) List(ctx context.Context, userID int64, status Status) ([]Relationship, error) {
	return s.store.list(ctx, userID, status)
}
