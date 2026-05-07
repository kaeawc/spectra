package main

import (
	"context"
	"fmt"
	"os"

	"github.com/kaeawc/spectra/internal/artifact"
)

var artifactRecorder artifact.Recorder

func initArtifactRecorder() {
	path, err := artifact.DefaultManifestPath()
	if err != nil {
		return
	}
	artifactRecorder = artifact.NewManager(artifact.NewJSONStore(path), nil)
}

func recordArtifactCLI(rec artifact.Record) {
	if artifactRecorder == nil {
		return
	}
	_, err := artifactRecorder.Record(context.Background(), rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "artifact manifest: %v\n", err)
		return
	}
}
