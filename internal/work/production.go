package work

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

func productionPrompt(p core.Production) string {
	var b strings.Builder
	b.WriteString("\nThis is a production request for finished assets. Generating images and attaching them with attach_file (generated) is expected where your tools allow it; otherwise attach SVG or escalate if raster art is required. This writes nothing to the repository. 'Don't deliver' means don't land, commit or approve; do not write to the workspace or repository.\nHand over complete groups of at most 10 assets per turn. The request stays with you until every wanted asset is delivered. For each asset call attach_file with asset set to its wanted name, and give its prompt, generator and settings, references and their hashes in the reply's provenance. Attach rejected variants with rejected: true; the daemon builds one archive per request.\nWanted assets:\n")
	for _, a := range p.Assets {
		fmt.Fprintf(&b, "- %s: %s\n", a.Name, a.Want)
	}
	fmt.Fprintf(&b, "Each asset's provenance can be at most %d KiB of JSON. Keep exact prompts and settings within that limit.\n", core.MaxAssetProvenanceBytes>>10)
	b.WriteString(roles.InlineAttachmentGuide + " Use generated for generated images when available.\n")
	b.WriteString(productionSummary(p))
	return b.String()
}

func productionAssetText(d core.DeliveredAsset) string {
	settings, _ := json.Marshal(d.Settings)
	return fmt.Sprintf("production asset %s, designer turn %d, SHA-256 %s, prompt: %s, generator: %s, settings: %s, references: %v", d.Asset, d.Turn, d.SHA256, d.Prompt, d.Generator, settings, d.References)
}

func productionSummary(p core.Production) string {
	var b strings.Builder
	b.WriteString("  Delivered assets (build from their attached files; copy into the workspace as needed):\n")
	for _, d := range p.Delivered {
		fmt.Fprintf(&b, "  - %s, attachment %s\n", productionAssetText(d), d.Attachment)
	}
	b.WriteString("  Remaining assets:\n")
	for _, a := range p.Remaining() {
		fmt.Fprintf(&b, "  - %s: %s\n", a.Name, a.Want)
	}
	if p.Archive != "" {
		fmt.Fprintf(&b, "  Rejected variants archive: %s\n", p.Archive)
	}
	if p.Provenance != "" {
		fmt.Fprintf(&b, "  provenance.json: %s\n", p.Provenance)
	}
	return b.String()
}
