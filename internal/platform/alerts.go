package platform

type AlertSettings struct {
	MinimumScore    int  `json:"minimum_score"`
	CooldownMinutes int  `json:"cooldown_minutes"`
	Telegram        bool `json:"telegram"`
	Slack           bool `json:"slack"`
	Email           bool `json:"email"`
}

func DefaultAlertSettings() AlertSettings {
	return AlertSettings{MinimumScore: 80, CooldownMinutes: 15, Telegram: true, Slack: true, Email: true}
}

func (s AlertSettings) Valid() bool {
	return s.MinimumScore >= 1 && s.MinimumScore <= 100 && s.CooldownMinutes >= 1 && s.CooldownMinutes <= 1440
}
