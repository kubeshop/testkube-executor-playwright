package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelseyhightower/envconfig"
	"github.com/kubeshop/testkube/pkg/api/v1/testkube"
	"github.com/kubeshop/testkube/pkg/executor"
	"github.com/kubeshop/testkube/pkg/executor/content"
	"github.com/kubeshop/testkube/pkg/executor/runner"
	"github.com/kubeshop/testkube/pkg/executor/scraper"
	"github.com/kubeshop/testkube/pkg/executor/secret"
)

type Params struct {
	Endpoint        string // RUNNER_ENDPOINT
	AccessKeyID     string // RUNNER_ACCESSKEYID
	SecretAccessKey string // RUNNER_SECRETACCESSKEY
	Location        string // RUNNER_LOCATION
	Token           string // RUNNER_TOKEN
	Ssl             bool   // RUNNER_SSL
	ScrapperEnabled bool   // RUNNER_SCRAPPERENABLED
	Datadir         string // RUNNER_DATADIR
}

func NewPlaywrightRunner(dependency string) (*PlaywrightRunner, error) {
	var params Params
	err := envconfig.Process("runner", &params)
	if err != nil {
		return nil, err
	}

	return &PlaywrightRunner{
		Params:  params,
		Fetcher: content.NewFetcher(""),
		Scraper: scraper.NewMinioScraper(
			params.Endpoint,
			params.AccessKeyID,
			params.SecretAccessKey,
			params.Location,
			params.Token,
			params.Ssl,
		),
		dependency: dependency,
	}, nil
}

// PlaywrightRunner - implements runner interface used in worker to start test execution
type PlaywrightRunner struct {
	Params     Params
	Fetcher    content.ContentFetcher
	Scraper    scraper.Scraper
	dependency string
}

func (r *PlaywrightRunner) Run(execution testkube.Execution) (result testkube.ExecutionResult, err error) {
	// check that the datadir exists
	_, err = os.Stat(r.Params.Datadir)
	if errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("Datadir not exist: %w\n\n", err)
	}

	runPath := filepath.Join(r.Params.Datadir, "repo", execution.Content.Repository.Path)
	if execution.Content.Repository != nil && execution.Content.Repository.WorkingDir != "" {
		runPath = filepath.Join(r.Params.Datadir, "repo", execution.Content.Repository.WorkingDir)
	}

	if _, err := os.Stat(filepath.Join(runPath, "package.json")); err == nil {
		out, err := executor.Run(runPath, r.dependency, nil, "install")
		if err != nil {
			return result, fmt.Errorf("%s install error: %w\n\n%s", r.dependency, err, out)
		}
	}

	runner := "npx"
	if r.dependency == "pnpm" {
		runner = "pnpx"
	}

	args := []string{"playwright", "test"}
	args = append(args, execution.Args...)

	out, err := executor.Run(runPath, runner, nil, args...)
	if err != nil {
		return result, fmt.Errorf("playwright test error: %w\n\n%s", err, out)
	}

	envManager := secret.NewEnvManagerWithVars(execution.Variables)
	out = envManager.Obfuscate(out)
	result = testkube.ExecutionResult{
		Status:     testkube.ExecutionStatusPassed,
		OutputType: "text/plain",
		Output:     string(out),
	}

	projectPath := filepath.Join(r.Params.Datadir, "repo", execution.Content.Repository.Path)
	if r.Params.ScrapperEnabled {
		if _, err := executor.Run(projectPath, "mkdir", nil, "playwright-report-zip"); err != nil {
			return result, fmt.Errorf("%s mkdir error: %w\n\n%s", r.dependency, err, out)
		}

		if _, err := executor.Run(projectPath, "zip", nil, "playwright-report-zip/playwright-report.zip", "-r", "playwright-report"); err != nil {
			return result, fmt.Errorf("%s zip error: %w\n\n%s", r.dependency, err, out)
		}

		directories := []string{
			filepath.Join(projectPath, "playwright-report-zip"),
		}
		err := r.Scraper.Scrape(execution.Id, directories)
		if err != nil {
			return result.WithErrors(fmt.Errorf("scrape artifacts error: %w", err)), nil
		}
	}

	return result, nil
}

// GetType returns runner type
func (r *PlaywrightRunner) GetType() runner.Type {
	return runner.TypeMain
}
