module github.com/hanzoai/git

go 1.26.4

// Native, embeddable Git hosting for the Hanzo cloud binary. Storage is
// go-git objects on a go-billy filesystem backed by hanzoai/vfs (→ S3), with
// hanzoai/base (SQLite) for repo metadata. Mounts into cloud via cloud.Register,
// exactly like hanzoai/ai. `go mod tidy` resolves the versions below from the
// same module graph the cloud binary already pins.
require (
	github.com/go-git/go-git/v5 v5.19.1
	github.com/hanzoai/base v1.4.1
	github.com/hanzoai/cloud v0.0.0
	github.com/hanzoai/vfs v0.4.1
	github.com/hanzoai/zip v0.5.0
	github.com/luxfi/log v1.0.0
)
