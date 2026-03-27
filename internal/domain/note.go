package domain

import "time"

type Note struct {
	CreatedAt time.Time
	UpdatedAt time.Time
	Title     string
	Content   string
	ID        int64
}
