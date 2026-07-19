package lsp

import (
	"context"
	"os"
)

// RenameApply performs a rename and returns the new full content of each affected file
// (absolute path -> content), applying the server's edits to the current on-disk text. Files
// that can't be read are skipped. The caller writes the results (and is responsible for
// validating the paths against its own sandbox before writing).
func (m *Manager) RenameApply(ctx context.Context, path string, line, char int, newName string) (map[string]string, error) {
	changes, err := m.Rename(ctx, path, line, char, newName)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(changes))
	for _, c := range changes {
		b, err := os.ReadFile(c.Path)
		if err != nil {
			continue
		}
		out[c.Path] = applyTextEdits(string(b), c.Edits)
	}
	return out, nil
}
