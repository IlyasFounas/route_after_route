// Package routeafterroute embeds the example files and the demo data, so the
// desktop binary can bootstrap a working setup on first launch.
package routeafterroute

import "embed"

// Assets holds examples/*.json and the synthetic demo markets of testdata/.
//
//go:embed examples/*.json testdata/trend testdata/range
var Assets embed.FS
