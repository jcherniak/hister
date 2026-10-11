package visual

import "time"

const (
	MaxResources     = 256
	MaxResourceBytes = 8 << 20
	MaxCaptureBytes  = 24 << 20
	MaxHTMLBytes     = 8 << 20
	MaxPixels        = 24_000_000
)

type Capture struct {
	HTML           string  `json:"html"`
	ViewportWidth  int     `json:"viewport_width"`
	ViewportHeight int     `json:"viewport_height"`
	ColorScheme    string  `json:"color_scheme,omitempty"`
	Resources      []Asset `json:"resources"`
}

type Asset struct {
	URL    string `json:"url"`
	MIME   string `json:"mime"`
	Kind   string `json:"kind"`
	Data   []byte `json:"data"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

type Resource struct {
	URL          string `json:"url"`
	Key          string `json:"key"`
	MIME         string `json:"mime"`
	Kind         string `json:"kind"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	StoredWidth  int    `json:"stored_width,omitempty"`
	StoredHeight int    `json:"stored_height,omitempty"`
}

type Manifest struct {
	Version        int        `json:"version"`
	HTMLKey        string     `json:"html_key"`
	CapturedAt     time.Time  `json:"captured_at"`
	ViewportWidth  int        `json:"viewport_width"`
	ViewportHeight int        `json:"viewport_height"`
	ColorScheme    string     `json:"color_scheme,omitempty"`
	Resources      []Resource `json:"resources"`
}

type Progress struct {
	Completed int `json:"completed"`
	Total     int `json:"total"`
}
