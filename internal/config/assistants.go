package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// AssistantProfile is one assistant the owner keeps on their team: who it
// is, how it looks and the model it runs on. The one in Assistant.Seat is
// the assistant the owner works with.
type AssistantProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Personality string `json:"personality"`
	Avatar      Avatar `json:"avatar"`
	Model       Model  `json:"model"`
	// Browser lets the assistant's chat use the owner's browser, as a
	// sandboxed session rather than one limited to the assistant's tools.
	Browser Browser `json:"browser,omitzero"`
}

// MaxAssistants is the most assistant profiles a config keeps.
const MaxAssistants = 32

var profileID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// DefaultProfile is the assistant a new config starts with, in the seat.
func DefaultProfile() AssistantProfile {
	return AssistantProfile{
		ID:          ProfileID(DefaultAssistantName),
		Name:        DefaultAssistantName,
		Personality: "Calm, concise and proactive. Bring clear recommendations and evidence; handle the chasing.",
		Avatar:      Avatar{Shape: "orb", Background: "#16211e", Accent: "#a8c5a8"},
		Model:       defaultModel(),
	}
}

// ProfileID is the id a profile converted from an earlier layout gets: its
// name in lower case, so converting the same file twice gives the same id.
func ProfileID(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	id := strings.Trim(b.String(), "-")
	if len(id) > 64 {
		id = strings.Trim(id[:64], "-")
	}
	if id == "" {
		return "assistant"
	}
	return id
}

// NewProfileID is a fresh id for a profile the owner adds.
func NewProfileID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// Validate checks one profile on its own; validateProfiles checks them
// together.
func (p AssistantProfile) Validate() error {
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 80 {
		return errors.New("an assistant's name must contain 1–80 characters")
	}
	if len(p.Personality) > 4000 {
		return fmt.Errorf("%s's personality must not exceed 4000 characters", p.Name)
	}
	if err := p.Avatar.Validate(); err != nil {
		return fmt.Errorf("%s's avatar: %w", p.Name, err)
	}
	if err := p.Model.Validate(); err != nil {
		return fmt.Errorf("%s's model: %w", p.Name, err)
	}
	if err := p.Browser.Validate(p.Model.Engine); err != nil {
		return fmt.Errorf("%s's browser: %w", p.Name, err)
	}
	return nil
}

func validateProfiles(profiles []AssistantProfile) error {
	if len(profiles) > MaxAssistants {
		return fmt.Errorf("at most %d assistants are supported", MaxAssistants)
	}
	for i, p := range profiles {
		if !profileID.MatchString(p.ID) {
			return errors.New("each assistant needs an id of 1–64 lower-case letters, digits or hyphens")
		}
		if err := p.Validate(); err != nil {
			return err
		}
		for _, earlier := range profiles[:i] {
			if earlier.ID == p.ID {
				return fmt.Errorf("two assistants have the id %s", p.ID)
			}
			if strings.EqualFold(strings.TrimSpace(earlier.Name), strings.TrimSpace(p.Name)) {
				return fmt.Errorf("there is already an assistant called %s", earlier.Name)
			}
		}
	}
	return nil
}

// Profile is the assistant profile with this id.
func (c Config) Profile(id string) (AssistantProfile, bool) {
	i := slices.IndexFunc(c.Assistants, func(p AssistantProfile) bool { return p.ID == id })
	if id == "" || i < 0 {
		return AssistantProfile{}, false
	}
	return c.Assistants[i], true
}

// ProfileRef is the assistant profile with this id, to change in place.
func (c *Config) ProfileRef(id string) *AssistantProfile {
	for i := range c.Assistants {
		if c.Assistants[i].ID == id {
			return &c.Assistants[i]
		}
	}
	return nil
}

// CloneAssistants is c with a copy of its profiles of its own, to change
// without changing c.
func (c Config) CloneAssistants() Config {
	c.Assistants = slices.Clone(c.Assistants)
	return c
}

// Seated is the assistant in the seat, if one is.
func (c Config) Seated() (AssistantProfile, bool) { return c.Profile(c.Assistant.Seat) }
