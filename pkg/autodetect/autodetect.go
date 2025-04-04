package autodetect

import (
	"context"

	"go.jetify.com/devbox/internal/devconfig"
	"go.jetify.com/devbox/pkg/autodetect/detector"
)

func InitConfig(ctx context.Context, path string) error {
	config, err := devconfig.Init(path)
	if err != nil {
		return err
	}

	if err = populateConfig(ctx, path, config); err != nil {
		return err
	}

	return config.Root.Save()
}

func DryRun(ctx context.Context, path string) ([]byte, error) {
	config := devconfig.DefaultConfig()
	if err := populateConfig(ctx, path, config); err != nil {
		return nil, err
	}
	return config.Root.Bytes(), nil
}

func populateConfig(ctx context.Context, path string, config *devconfig.Config) error {
	pkgs, err := packages(ctx, path)
	if err != nil {
		return err
	}
	for _, pkg := range pkgs {
		config.PackageMutator().Add(pkg)
	}
	env, err := env(ctx, path)
	if err != nil {
		return err
	}
	config.Root.SetEnv(env)
	return nil
}

func detectors(path string) []detector.Detector {
	return []detector.Detector{
		&detector.GoDetector{Root: path},
		&detector.NodeJSDetector{Root: path},
		&detector.PHPDetector{Root: path},
		&detector.PoetryDetector{Root: path},
		&detector.PythonDetector{Root: path},
	}
}

func packages(ctx context.Context, path string) ([]string, error) {
	mostRelevantDetector, err := relevantDetector(path)
	if err != nil || mostRelevantDetector == nil {
		return nil, err
	}
	return mostRelevantDetector.Packages(ctx)
}

func env(ctx context.Context, path string) (map[string]string, error) {
	mostRelevantDetector, err := relevantDetector(path)
	if err != nil || mostRelevantDetector == nil {
		return nil, err
	}
	return mostRelevantDetector.Env(ctx)
}

// relevantDetector returns the most relevant detector for the given path.
// We could modify this to return a list of detectors and their scores or
// possibly grouped detectors by category (e.g. python, server, etc.)
func relevantDetector(path string) (detector.Detector, error) {
	relevantScore := 0.0
	var mostRelevantDetector detector.Detector
	for _, detector := range detectors(path) {
		if d, ok := detector.(interface {
			Init() error
		}); ok {
			if err := d.Init(); err != nil {
				return nil, err
			}
		}
		score, err := detector.Relevance(path)
		if err != nil {
			return nil, err
		}
		if score > relevantScore {
			relevantScore = score
			mostRelevantDetector = detector
		}
	}
	return mostRelevantDetector, nil
}
