package issueworkflow

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Artifact is the immutable code revision handed between ordinary workflow
// steps. It is carried by status writes, not a second step-result protocol.
type Artifact struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func (a Artifact) Validate() error {
	if !commitID.MatchString(a.Commit) || strings.TrimSpace(a.Repo) == "" || len(a.Repo) > 1000 || len(a.Branch) > 300 {
		return errors.New("artifact requires a full commit SHA and repository; repository/branch are limited to 1000/300 characters")
	}
	if strings.ContainsAny(a.Repo+a.Branch, "\x00\r\n") || strings.HasPrefix(a.Branch, "-") {
		return errors.New("invalid artifact repository or branch")
	}
	return nil
}

func TaskArtifact(raw []byte) *Artifact {
	var data struct {
		Artifact *Artifact `json:"workflow_artifact"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Artifact == nil || data.Artifact.Validate() != nil {
		return nil
	}
	return data.Artifact
}
