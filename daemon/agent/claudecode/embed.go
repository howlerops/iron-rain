package claudecode

import _ "embed"

// The sidecar's source files, baked into the daemon binary so it can materialize +
// `npm install` the sidecar on a fresh machine (node_modules is NOT embedded — it's
// fetched by npm at setup time). Kept in sync with sidecar/sidecar.mjs + package.json.

//go:embed sidecar/sidecar.mjs
var SidecarMJS []byte

//go:embed sidecar/package.json
var SidecarPackageJSON []byte
