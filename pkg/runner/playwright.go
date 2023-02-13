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
		return result, fmt.Errorf("Datadir not exist: %w", err)
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

	envManager := secret.NewEnvManagerWithVars(execution.Variables)
	envManager.GetVars(envManager.Variables)

	out, err := executor.Run(runPath, runner, envManager, args...)
	if err != nil {
		return result, fmt.Errorf("playwright test error: %w\n\n%s", err, out)
	}

	out = envManager.Obfuscate(out)
	result = testkube.ExecutionResult{
		Status:     testkube.ExecutionStatusPassed,
		OutputType: "text/plain",
		Output:     string(out),
	}

	if r.Params.ScrapperEnabled {
		if err = scrapeArtifacts(r, execution); err != nil {
			return result, err
		}
	}

	return result, nil
}

// GetType returns runner type
func (r *PlaywrightRunner) GetType() runner.Type {
	return runner.TypeMain
}

func scrapeArtifacts(r *PlaywrightRunner, execution testkube.Execution) (err error) {
	projectPath := filepath.Join(r.Params.Datadir, "repo", execution.Content.Repository.Path)

	originalName := "playwright-report"
	compressedName := originalName + "-zip"

	if _, err := executor.Run(projectPath, "mkdir", nil, compressedName); err != nil {
		return fmt.Errorf("mkdir error: %w", err)
	}

	if _, err := executor.Run(projectPath, "zip", nil, compressedName+"/"+originalName+".zip", "-r", originalName); err != nil {
		return fmt.Errorf("zip error: %w", err)
	}

	directories := []string{
		filepath.Join(projectPath, compressedName),
	}
	if err := r.Scraper.Scrape(execution.Id, directories); err != nil {
		return fmt.Errorf("scrape artifacts error: %w", err)
	}

	return nil
}
