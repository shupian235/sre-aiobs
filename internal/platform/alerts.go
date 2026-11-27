package platform

import (
	"net/mail"
	"regexp"
	"strings"
)

type AlertSettings struct {
	MinimumScore     int               `json:"minimum_score"`
	CooldownMinutes  int               `json:"cooldown_minutes"`
	Telegram         bool              `json:"telegram"`
	Slack            bool              `json:"slack"`
	Email            bool              `json:"email"`
	EmailDestination *EmailDestination `json:"email_destination,omitempty"`
}

type EmailDestination struct {
	From   string   `json:"from"`
	To     []string `json:"to"`
	Region string   `json:"region"`
}

var awsRegion = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)

func (d *EmailDestination) Configured() bool {
	return d != nil && d.From != "" && len(d.To) > 0
}

func (d *EmailDestination) valid() bool {
	if d == nil {
		return true
	}
	if (d.From != "" && !validMailbox(d.From)) || len(d.To) > 50 ||
		(d.Region != "" && (len(d.Region) > 40 || !awsRegion.MatchString(d.Region))) {
		return false
	}
	for _, addr := range d.To {
		if !validMailbox(addr) {
			return false
		}
	}
	return true
}

func validMailbox(value string) bool {
	if len(value) > 254 || strings.IndexFunc(value, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Name == "" && address.Address == value
}

func DefaultAlertSettings() AlertSettings {
	return AlertSettings{MinimumScore: 80, CooldownMinutes: 15, Telegram: true, Slack: true, Email: true}
}

func (s AlertSettings) Valid() bool {
	return s.MinimumScore >= 1 && s.MinimumScore <= 100 && s.CooldownMinutes >= 1 && s.CooldownMinutes <= 1440 && s.EmailDestination.valid()
}
