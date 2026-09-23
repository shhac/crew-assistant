// Package media holds what every medium shows the owner.
package media

// File is one file of a revision as shown to the owner.
type File struct {
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Size      int64  `json:"size"`
}
