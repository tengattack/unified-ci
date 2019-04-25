package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/bradleyfalzon/ghinstallation"
	"github.com/google/go-github/github"
	"github.com/pkg/errors"
	yaml "gopkg.in/yaml.v2"
)

const (
	projectTestsConfigFile = ".unified-ci.yml"
)

var (
	percentageRegexp = regexp.MustCompile(`[-+]?(?:\d*\.\d+|\d+)%`)
)

type panicError struct {
	Info interface{}
}

func (p *panicError) Error() (s string) {
	if p != nil {
		s = fmt.Sprintf("Panic: %v", p.Info)
	}
	return
}

// InitHTTPRequest helps to set necessary headers
func InitHTTPRequest(req *http.Request, isJSONResponse bool) {
	if isJSONResponse {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", UserAgent())
}

// DoHTTPRequest sends request and gets response to struct
func DoHTTPRequest(req *http.Request, isJSONResponse bool, v interface{}) error {
	InitHTTPRequest(req, isJSONResponse)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	// close response
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	LogAccess.Debugf("HTTP %s\n%s", resp.Status, body)

	if isJSONResponse {
		err = json.Unmarshal(body, &v)
		if err != nil && resp.StatusCode != 200 {
			return errors.New("HTTP " + resp.Status)
		}
	} else {
		if ret, ok := v.(*[]byte); ok {
			*ret = body
		}
	}

	return err
}

// UpdateCheckRunWithError updates the check run result with error message
func UpdateCheckRunWithError(ctx context.Context, client *github.Client, gpull *github.PullRequest, checkRunID int64, checkName, outputTitle string, err error) {
	if gpull != nil {
		conclusion := "action_required"
		checkRunStatus := "completed"
		t := github.Timestamp{Time: time.Now()}
		outputSummary := fmt.Sprintf("error: %v", err)

		owner := gpull.GetBase().GetRepo().GetOwner().GetLogin()
		repo := gpull.GetBase().GetRepo().GetName()
		_, _, eror := client.Checks.UpdateCheckRun(ctx, owner, repo, checkRunID, github.UpdateCheckRunOptions{
			Name:        checkName,
			Status:      &checkRunStatus,
			Conclusion:  &conclusion,
			CompletedAt: &t,
			Output: &github.CheckRunOutput{
				Title:   &outputTitle,
				Summary: &outputSummary,
			},
		})
		if eror != nil {
			LogError.Errorf("github update check run with error failed: %v", eror)
		}
	}
}

// UpdateCheckRun updates the check run result with output message
// outputTitle, outputSummary can contain markdown.
func UpdateCheckRun(ctx context.Context, client *github.Client, gpull *github.PullRequest, checkRunID int64, checkName string, conclusion string, t github.Timestamp, outputTitle string, outputSummary string, annotations []*github.CheckRunAnnotation) error {
	checkRunStatus := "completed"

	owner := gpull.GetBase().GetRepo().GetOwner().GetLogin()
	repo := gpull.GetBase().GetRepo().GetName()
	_, _, err := client.Checks.UpdateCheckRun(ctx, owner, repo, checkRunID, github.UpdateCheckRunOptions{
		Name:        checkName,
		Status:      &checkRunStatus,
		Conclusion:  &conclusion,
		CompletedAt: &t,
		Output: &github.CheckRunOutput{
			Title:       &outputTitle,
			Summary:     &outputSummary,
			Annotations: annotations,
		},
	})
	if err != nil {
		LogError.Errorf("github update check run failed: %v", err)
	}
	return err
}

// CreateCheckRun creates a new check run
func CreateCheckRun(ctx context.Context, client *github.Client, gpull *github.PullRequest, checkName string, ref GithubRef, targetURL string) (*github.CheckRun, error) {
	checkRunStatus := "in_progress"

	owner := gpull.GetBase().GetRepo().GetOwner().GetLogin()
	repo := gpull.GetBase().GetRepo().GetName()
	checkRun, _, err := client.Checks.CreateCheckRun(ctx, owner, repo, github.CreateCheckRunOptions{
		Name:       checkName,
		HeadBranch: gpull.GetBase().GetRef(),
		HeadSHA:    ref.Sha,
		DetailsURL: &targetURL,
		Status:     &checkRunStatus,
	})
	return checkRun, err
}

type goTestsConfig struct {
	Coverage string   `yaml:"coverage"`
	Cmds     []string `yaml:"cmds"`
}

func emptyTest(cmds []string) bool {
	empty := true
	for _, c := range cmds {
		if c != "" {
			empty = false
		}
	}
	return empty
}

func getTests(cwd string) (map[string]goTestsConfig, error) {
	content, err := ioutil.ReadFile(filepath.Join(cwd, projectTestsConfigFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var config struct {
		Tests map[string]goTestsConfig `yaml:"tests"`
	}
	err = yaml.Unmarshal(content, &config)
	if err != nil {
		var cfg struct {
			Tests map[string][]string `yaml:"tests"`
		}
		err = yaml.Unmarshal(content, &cfg)
		if err != nil {
			return nil, err
		}
		config.Tests = make(map[string]goTestsConfig)
		for k, v := range cfg.Tests {
			config.Tests[k] = goTestsConfig{Cmds: v, Coverage: ""}
		}
	}
	return config.Tests, nil
}

func getDefaultAPIClient(owner string) (*github.Client, error) {
	var client *github.Client
	installationID, ok := Conf.GitHub.Installations[owner]
	if ok {
		tr, err := ghinstallation.NewKeyFromFile(http.DefaultTransport,
			Conf.GitHub.AppID, installationID, Conf.GitHub.PrivateKey)
		if err != nil {
			return nil, err
		}

		client = github.NewClient(&http.Client{Transport: tr})
		return client, nil
	}
	return nil, errors.New("InstallationID not found, owner: " + owner)
}
