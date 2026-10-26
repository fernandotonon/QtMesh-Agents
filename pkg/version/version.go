package version

// Build-time variables set via -ldflags.
var (
	Version = "0.1.0-dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a human-readable version string.
func String() string {
	return Version + " (" + Commit + " " + Date + ")"
}
