package model

import (
	"net/http"
)

type HttpStatus struct {
	ID     string `firestore:"-" firestore_key:"auto"`
	Status string `firestore:"status"`
}

func (s *HttpStatus) UpdateStatusCode(statusCode int) {
	if statusCode == http.StatusOK {
		s.Status = "OK"
		return
	}
	s.Status = "Error"
}
