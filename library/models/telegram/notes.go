// Package telegram defines the data model for the blog service.
package telegram

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TelegramNote is the model for telegram notes
type TelegramNote struct {
	ID        primitive.ObjectID    `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt time.Time             `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time             `bson:"updated_at" json:"updated_at"`
	Content   string                `bson:"content" json:"content"`
	PostID    int                   `bson:"post_id" json:"post_id"`
	ArweaveID string                `bson:"arweave_id" json:"arweave_id"`
	Digest    string                `bson:"digest" json:"digest"`
	History   []TelegramNoteHistory `bson:"history" json:"history"`
}

// TelegramNoteHistory is the model for telegram note history
type TelegramNoteHistory struct {
	Content   string    `bson:"content" json:"content"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	Digest    string    `bson:"digest" json:"digest"`
}

// UpdateNote update note content
func (n *TelegramNote) UpdateNote(content string) (changed bool) {
	// check current content's digest
	newHashed := sha256.Sum256([]byte(content))
	newDigest := hex.EncodeToString(newHashed[:])

	if n.Digest == "" {
		hashed := sha256.Sum256([]byte(n.Content))
		n.Digest = hex.EncodeToString(hashed[:])
	}

	if n.Digest == newDigest {
		return false
	}

	// update note
	n.History = append(n.History, TelegramNoteHistory{
		Content:   n.Content,
		CreatedAt: n.UpdatedAt,
		Digest:    n.Digest,
	})
	n.Content = content
	n.UpdatedAt = time.Now()
	n.Digest = newDigest

	return true
}
