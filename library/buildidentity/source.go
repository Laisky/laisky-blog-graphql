// Package buildidentity reports application-selected public build and process metadata.
package buildidentity

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"runtime/debug"
	"sync"

	"github.com/Laisky/errors/v2"
)

// Source identifies the application process without inspecting configuration or payloads.
type Source struct {
	Service       string `json:"service"`
	Hostname      string `json:"hostname"`
	ProcessRunID  string `json:"process_run_id"`
	Revision      string `json:"revision,omitempty"`
	Modified      bool   `json:"modified"`
	GoVersion     string `json:"go_version,omitempty"`
	ModuleVersion string `json:"module_version,omitempty"`
}

var processIdentity struct {
	sync.Once
	id  string
	err error
}

// randomProcessRunID creates a process correlation identifier, not a credential.
func randomProcessRunID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", errors.Wrap(err, "create process identity")
	}
	return hex.EncodeToString(data[:]), nil
}

// ProcessSource selects public build/process identity without inspecting configuration.
func ProcessSource(service string) (Source, error) {
	processIdentity.Do(func() { processIdentity.id, processIdentity.err = randomProcessRunID() })
	if processIdentity.err != nil {
		return Source{}, processIdentity.err
	}
	host, err := os.Hostname()
	if err != nil {
		return Source{}, errors.Wrap(err, "identify application host")
	}
	source := Source{Service: service, Hostname: host, ProcessRunID: processIdentity.id}
	if info, ok := debug.ReadBuildInfo(); ok {
		source.GoVersion = info.GoVersion
		source.ModuleVersion = info.Main.Version
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				source.Revision = setting.Value
			case "vcs.modified":
				source.Modified = setting.Value == "true"
			}
		}
	}
	return source, nil
}
