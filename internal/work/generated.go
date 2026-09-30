package work

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
)

// generatesImages is whether a role's engine can generate images: Codex
// can, and the role sandbox leaves it on.
func generatesImages(r core.Role) bool {
	return harness.Engine(r.Engine) == harness.Codex
}

// generatedRoot is where Codex saves the images every role's session
// generates, one folder a session.
func (lp *Loop) generatedRoot() string {
	return filepath.Join(lp.runtimeHome(string(harness.Codex)), "generated_images")
}

// generatedImages is where Codex saves the images a designing turn
// generates: <runtime home>/generated_images/<thread>/. A turn's tools are
// made before its session opens, so the folder is recorded once it does,
// and read by the tools' handler while the turn runs.
type generatedImages struct {
	home string
	mu   sync.Mutex
	// dir is the folder of the session running now, or empty when it is not
	// known; ended are the folders of the turn's sessions confirmed gone,
	// removed once the turn is over. A session not confirmed gone may still
	// be running, so its folder stays until a restart reclaims it.
	dir   string
	ended []string
}

// threadID is what a Codex session's id looks like, as the avatar painter
// checks it, so an id can never lead out of generated_images.
var threadID = regexp.MustCompile(`^[0-9a-fA-F-]{16,64}$`)

// opened records the folder of the session a turn now runs in.
func (g *generatedImages) opened(ref session.Ref) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.dir = ""
	if !threadID.MatchString(ref.ID) {
		return
	}
	g.dir = filepath.Join(g.home, "generated_images", ref.ID)
}

// closed hears that the session running now is over: its folder may go
// with the turn only if it was confirmed gone.
func (g *generatedImages) closed(confirmed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if confirmed && g.dir != "" && !slices.Contains(g.ended, g.dir) {
		g.ended = append(g.ended, g.dir)
	}
	g.dir = ""
}

// remove deletes the images the turn's sessions confirmed gone generated;
// any the designer attached are kept with the task already.
func (g *generatedImages) remove() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, dir := range g.ended {
		os.RemoveAll(dir)
	}
	g.dir, g.ended = "", nil
}

const generatedWhere = "the images you generated in this turn"

// read reads an image generated in this turn, named by its plain file name
// only: a path of any kind, even one into this turn's folder, is refused, so
// nothing outside that folder is ever read.
func (g *generatedImages) read(name string) ([]byte, string, error) {
	if g == nil {
		return nil, "", errors.New("image generation isn't available to you here; attach a sketch by content or path instead")
	}
	g.mu.Lock()
	dir := g.dir
	g.mu.Unlock()
	if dir == "" {
		return nil, "", errors.New("the images generated in this turn can't be found; attach a sketch by content or path instead")
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) || filepath.IsAbs(name) {
		return nil, "", fmt.Errorf("generated takes the file name of an image in %s, not a path", generatedWhere)
	}
	have := generatedNames(dir)
	if len(have) == 0 {
		return nil, "", errors.New("you have generated no images in this turn")
	}
	if !slices.Contains(have, name) {
		return nil, "", fmt.Errorf("there is no image %s among %s: %s", name, generatedWhere, strings.Join(have, ", "))
	}
	data, err := readInside(dir, name, generatedWhere)
	return data, name, err
}

// generatedNames are the plain files in a folder of generated images.
func generatedNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	return names
}
