package dao

import (
	"context"

	"github.com/Laisky/errors/v2"
	"go.mongodb.org/mongo-driver/bson"
	mongoLib "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
)

const (
	telegramColNotes = "notes"
)

// Telegram db
type Telegram struct {
	db mongo.DB
}

// New create new DB
func NewTelegram(db mongo.DB) *Telegram {
	return &Telegram{db}
}

// GetNotesCol get notes collection
func (d *Telegram) GetNotesCol() *mongoLib.Collection {
	return d.db.GetCol(telegramColNotes)
}

// Search search notes by keyword
func (d *Telegram) Search(ctx context.Context, keyword string) (notes []*model.TelegramNote, err error) {
	cur, err := d.GetNotesCol().Find(ctx,
		bson.M{"content": bson.M{"$regex": keyword}},
		options.Find().SetSort(bson.M{"_id": -1}).SetLimit(10),
	)
	if err != nil {
		return nil, errors.Wrap(err, "search notes")
	}
	defer cur.Close(ctx)

	for cur.Next(ctx) {
		note := &model.TelegramNote{}
		if err = cur.Decode(note); err != nil {
			return nil, errors.Wrap(err, "decode note")
		}
		notes = append(notes, note)
	}

	return notes, nil
}
