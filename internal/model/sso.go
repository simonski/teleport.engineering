package model

import "time"

type GoogleSSOConfig struct {
	Version      int64  `json:"version"`
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	RedirectURI  string `json:"redirectUri"`
	Mode         string `json:"mode"`
}

type GoogleEnrollment struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	AccountID    string    `json:"accountId,omitempty"`
	RequestedAt  time.Time `json:"requestedAt"`
	ApprovedBy   string    `json:"approvedBy,omitempty"`
	ApprovedAt   time.Time `json:"approvedAt,omitzero"`
	AdminGrantBy string    `json:"adminGrantBy,omitempty"`
	AdminGrantAt time.Time `json:"adminGrantAt,omitzero"`
}
