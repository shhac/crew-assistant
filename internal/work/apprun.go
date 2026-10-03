package work

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

// appRun is the daemon-hosted app for one QA turn, from the project's
// run recipe: on a port of its own, reached on this machine only, and
// through the engine's own browser when its seat says so. Without a recipe,
// or with a checker that isn't QA, it is the zero value and QA's check is
// exactly as it was.
type appRun struct {
	recipe  *core.RunRecipe
	port    int
	browser core.Browser
	// images is whether the engine hands back the screenshots it takes.
	images bool
	// unavailable says why QA can't run the app on its engine, which its
	// verdict then says; QA runs the check alone and is never given wider
	// network access instead.
	unavailable string
	release     func()
	// tree is the writable copy of the revision the app's commands run in,
	// in QA's scratch folder, so setup can build without touching the
	// revision checked.
	tree            string
	sandbox         commandSandbox
	process         startedCommand
	sessionLoopback bool
	log             string
	cleanupError    func(error)
}

// running reports whether the app is available for QA in this check.
func (a appRun) running() bool { return a.recipe != nil && a.unavailable == "" }

// planApp works out how a checker runs the app for one check, reserving it a
// port if it does. release gives the port back, once the check's copy is
// gone.
func (lp *Loop) planApp(checker core.Role, playbook *core.Playbook) (appRun, error) {
	none := appRun{release: func() {}}
	if !checker.Holds(core.RoleQA) || playbook == nil || playbook.Run == nil {
		return none, nil
	}
	run := none
	run.recipe = playbook.Run
	browser := checker.Browser
	if !browser.On {
		browser = lp.memberBrowser(checker)
	}
	if !(browser.On && config.Supports(checker.Engine, config.UseBrowser)) && !config.Supports(checker.Engine, config.UseLoopback) {
		c := harness.Support(harness.Engine(checker.Engine), harness.Session, harness.Loopback)
		run.unavailable = fmt.Sprintf("The app was not run: %s QA needs an allowed browser or session localhost access (%s). QA ran the check only.", config.EngineLabel(checker.Engine), strings.TrimSpace(c.Reason))
		return run, nil
	}
	port, err := lp.ports.reserve()
	if err != nil {
		return none, fmt.Errorf("finding a port for the app: %w", err)
	}
	run.port, run.release = port, func() { lp.ports.free(port) }
	if browser.On && config.Supports(checker.Engine, config.UseBrowser) {
		run.browser = browser
	}
	run.sessionLoopback = !run.browser.On && config.Supports(checker.Engine, config.UseLoopback)
	run.images = harness.Support(harness.Engine(checker.Engine), harness.Session, harness.ToolImages).Usable()
	return run, nil
}

// apply gives QA its allowed browser, or session localhost for requesting
// the daemon-hosted app where the engine offers it.
func (a appRun) apply(spec *roles.Spec) {
	if !a.running() {
		return
	}
	spec.Loopback = a.sessionLoopback
	// The member may already allow the browser everywhere; QA's own setting
	// adds it for using the app, never takes it away.
	spec.Browser = spec.Browser || a.browser.On
}

// ports are the loopback ports held by checks under way, so no two checks
// running side by side start their apps on the same one.
type ports struct {
	mu   sync.Mutex
	held map[int]bool
	// find picks a free port on this machine; nil asks the system.
	find func() (int, error)
}

// reserve holds a free port for a check until it is freed.
func (p *ports) reserve() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	find := p.find
	if find == nil {
		find = freeLoopbackPort
	}
	if p.held == nil {
		p.held = map[int]bool{}
	}
	for range 20 {
		port, err := find()
		if err != nil {
			return 0, err
		}
		if !p.held[port] {
			p.held[port] = true
			return port, nil
		}
	}
	return 0, errors.New("no free port could be found that another check isn't holding")
}

func (p *ports) free(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.held, port)
}

// freeLoopbackPort is a port no one is listening on now.
func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// screenshots hears QA's turn as whoever else watches it does, keeping the
// images its tools returned, such as the browser's screenshots, to keep
// with its verdict.
type screenshots struct {
	next    roles.Observer
	mu      sync.Mutex
	images  []session.Image
	omitted int
}

func (s *screenshots) Started() {
	if s.next != nil {
		s.next.Started()
	}
}

// Note forwards turn notes through QA's screenshot observer.
func (s *screenshots) Note(text string) {
	if next, ok := s.next.(interface{ Note(string) }); ok {
		next.Note(text)
	}
}

func (s *screenshots) Asked(prompt string) {
	if s.next != nil {
		s.next.Asked(prompt)
	}
}

func (s *screenshots) Saw(e session.Event) {
	if e.Kind == "tool_completed" && (len(e.Images) > 0 || e.ImagesOmitted > 0) {
		s.mu.Lock()
		s.images = append(s.images, e.Images...)
		s.omitted += e.ImagesOmitted
		// Only the last few are kept, so what came before is let go now.
		if extra := len(s.images) - core.MaxScreenshots; extra > 0 {
			s.images = append([]session.Image(nil), s.images[extra:]...)
			s.omitted += extra
		}
		s.mu.Unlock()
	}
	if s.next != nil {
		s.next.Saw(e)
	}
}

func (s *screenshots) Ended() {
	if s.next != nil {
		s.next.Ended()
	}
}

// taken is the last screenshots of the turn, and how many more it took that
// are not kept.
func (s *screenshots) taken() ([]session.Image, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.Image(nil), s.images...), s.omitted
}

var imageExtensions = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// files are the screenshots QA's turn took, as files to keep with its
// verdict when it is recorded, and how many more it took that are not kept,
// counting images of a type that can't be.
func (s *screenshots) files(by string) core.Screenshots {
	images, omitted := s.taken()
	out := core.Screenshots{By: by, Omitted: omitted}
	for _, img := range images {
		ext, ok := imageExtensions[img.MediaType]
		if !ok {
			out.Omitted++
			continue
		}
		out.Files = append(out.Files, core.NewFile{Name: fmt.Sprintf("screenshot-%d%s", len(out.Files)+1, ext), Data: img.Data})
	}
	return out
}
