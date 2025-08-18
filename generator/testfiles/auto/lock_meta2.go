package model

import "github.com/fujiwara-manabu-0516/volcago/generator/testfiles/auto/meta"

//go:generate volcago LockMeta2

// LockMeta2 ID自動生成あり
type LockMeta2 struct {
	ID   string             `firestore:"-" firestore_key:"auto"`
	Text string             `firestore:"text"`
	Flag map[string]float64 `firestore:"flag"`
	meta.AAAMeta
}
