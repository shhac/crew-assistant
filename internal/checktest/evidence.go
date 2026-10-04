package checktest

import (
	"bytes"
	"encoding/json"
)

// Reserve ample room below the task's 2 MiB/600-step retention bounds for
// JSON escaping, note metadata and terminal stage/count diagnostics. Stop
// ingestion before this budget, rather than evicting previously seen skips.
const MaxSkipEvidenceBytes = 256 << 10

func SkipEvidenceSize(skip Skip) int {
	var entry bytes.Buffer
	skip.Write(&entry)
	encoded, _ := json.Marshal(entry.String())
	return len(encoded)
}
