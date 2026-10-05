// Package web provides shared web assets for PrintMaster UIs.
package web

import _ "embed"

// SharedCSS contains the common stylesheet used by both agent and server UIs.
// This is embedded at compile time from shared.css and served as /static/shared.css
//
//go:embed shared.css
var SharedCSS string

// SharedJS contains common JavaScript utilities used by both agent and server UIs.
// This is embedded at compile time from shared.js and served as /static/shared.js
//
//go:embed shared.js
var SharedJS string

// MetricsJS contains the shared metrics UI implementation extracted from the agent UI.
// It is embedded at compile time from metrics.js and served as /static/metrics.js
//
//go:embed metrics.js
var MetricsJS string

// CardsJS contains shared card rendering and related helpers (renderSavedCard,
// checkDatabaseRotationWarning). Embedded from cards.js and served as
// /static/cards.js
//
//go:embed cards.js
var CardsJS string

// ProgressiveLoaderJS is the framework-free demand-driven inventory controller.
//
//go:embed progressive-loader.js
var ProgressiveLoaderJS string

// For CSP and offline scenarios we now vendor flatpickr under static/flatpickr
// and embed the files into the binary so they are served with correct MIME
// types. These variables expose the embedded assets which are served as
// /static/flatpickr/flatpickr.min.js, /static/flatpickr/flatpickr.min.css,
// and /static/flatpickr/LICENSE.md respectively.
//
//go:embed flatpickr/flatpickr.min.js
var FlatpickrJS string

//go:embed flatpickr/flatpickr.min.css
var FlatpickrCSS string

//go:embed flatpickr/LICENSE.md
var FlatpickrLicense string

// ReportJS contains the device data issue reporting UI component.
// Embedded from report.js and served as /static/report.js
//
//go:embed report.js
var ReportJS string
