package serve

import "embed"

// assetFS holds the review-console frontend, compiled into the binary so
// `culi serve` needs no external files. index.html loads seed.js (standalone
// fallback data) then app.js; every /api/* route overrides the seed at runtime.
//
//go:embed assets/*
var assetFS embed.FS
