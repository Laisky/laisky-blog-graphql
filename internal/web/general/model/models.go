// Package model contains all the models used in the application.
package model

import "time"

type Lock struct {
	Name      string    `firestore:"name"`
	OwnerID   string    `firestore:"owner_id"`
	ExpiresAt time.Time `firestore:"expires_at"`
}
