package checker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"time"

	"github.com/google/go-github/github"
	shellwords "github.com/mattn/go-shellwords"
	"github.com/tengattack/unified-ci/store"
	"github.com/tengattack/unified-ci/util"
)

type testNotPass struct {
	Title string
}

func (t *testNotPass) Error() (s string) {
	if t != nil {
		return t.Title
	}
	return
}

func carry(ctx context.Context, p *shellwords.Parser, repo, cmd string) (string, error) {
	words, err := p.Parse(cmd)
	if err != nil {
		return "", err
	}
	if len(words) < 1 {
		return "", errors.New("invalid command")
	}

	cmds := exec.CommandContext(ctx, words[0], words[1:]...)
	cmds.Dir = repo
	out, err := cmds.CombinedOutput()
	return string(out), err
}

// ReportTestResults reports the test results to github
func ReportTestResults(testName string, repoPath string, cmds []string, coveragePattern string, client *github.Client, gpull *github.PullRequest,
	ref GithubRef, targetURL string, log io.Writer) (string, error) {
	outputTitle := testName + " test"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	t := github.Timestamp{Time: time.Now()}

	var checkRunID int64
	if ref.isTree() {
		err := ref.UpdateState(client, outputTitle, "pending", targetURL, "running")
		if err != nil {
			msg := fmt.Sprintf("Update commit state %s failed: %v", outputTitle, err)
			_, _ = io.WriteString(log, msg+"\n")
			LogError.Error(msg)
			// PASS
		}
	} else {
		checkRun, err := CreateCheckRun(ctx, client, gpull, outputTitle, ref, targetURL)
		if err != nil {
			msg := fmt.Sprintf("Creating %s check run failed: %v", outputTitle, err)
			_, _ = io.WriteString(log, msg+"\n")
			LogError.Error(msg)
			return "", err
		}
		checkRunID = checkRun.GetID()
	}

	conclusion, reportMessage, outputSummary := testAndSaveCoverage(ctx, ref, testName, cmds,
		coveragePattern, repoPath, gpull, false, log)

	title := ""
	if coveragePattern == "" {
		title = conclusion
	} else {
		title = "coverage: " + reportMessage
	}
	if ref.isTree() {
		state := "success"
		if conclusion == "failure" {
			state = "error"
		}
		err := ref.UpdateState(client, outputTitle, state, targetURL, title)
		if err != nil {
			msg := fmt.Sprintf("Update commit state %s failed: %v", outputTitle, err)
			_, _ = io.WriteString(log, msg+"\n")
			LogError.Error(msg)
			// PASS
		}
	} else {
		err := UpdateCheckRun(ctx, client, gpull, checkRunID, outputTitle, conclusion, t, title, "```\n"+outputSummary+"\n```", nil)
		if err != nil {
			LogError.Errorf("report test results to github failed: %v", err)
			// PASS
		}
	}
	if conclusion == "failure" {
		err := &testNotPass{Title: outputTitle}
		return reportMessage, err
	}
	return reportMessage, nil
}

func parseCoverage(pattern, output string) (string, float64, error) {
	coverage := "unknown"
	r, err := regexp.Compile(pattern)
	if err != nil {
		return "error", 0, err
	}
	match := r.FindStringSubmatch(output)
	if len(match) > 1 {
		coverage = match[1]
	}
	pct, err := util.ParseFloatPercent(coverage, 64)
	if err != nil {
		return coverage, 0, err
	}
	return coverage, pct, nil
}

func testAndSaveCoverage(ctx context.Context, ref GithubRef, testName string, cmds []string, coveragePattern string,
	repoPath string, gpull *github.PullRequest, breakOnFails bool, log io.Writer) (conclusion, reportMessage, outputSummary string) {
	parser := NewShellParser(repoPath, ref)

	_, _ = io.WriteString(log, fmt.Sprintf("Testing '%s'\n", testName))
	conclusion = "success"
	for _, cmd := range cmds {
		if cmd != "" {
			out, errCmd := carry(ctx, parser, repoPath, cmd)
			msg := cmd + "\n" + out + "\n"
			if errCmd != nil {
				msg += errCmd.Error() + "\n"
			}

			_, _ = io.WriteString(log, msg)
			outputSummary += msg
			if errCmd != nil {
				conclusion = "failure"
				if breakOnFails {
					break
				}
			}
		}
	}
	// get test coverage even if the conclusion is failure when ignoring the failed tests
	if coveragePattern != "" && (conclusion == "success" || !breakOnFails) {
		percentage, pct, err := parseCoverage(coveragePattern, outputSummary)
		if err != nil {
			msg := fmt.Sprintf("Failed to parse %s test coverage: %v\n", testName, err)
			LogError.Error(msg)
			_, _ = io.WriteString(log, msg)
			// PASS
		}
		if err == nil || ref.isTree() {
			c := store.CommitsInfo{
				Owner:    ref.owner,
				Repo:     ref.repo,
				Sha:      ref.Sha,
				Author:   gpull.GetHead().GetUser().GetLogin(),
				Test:     testName,
				Coverage: &pct,
			}
			if conclusion == "success" {
				c.Passing = 1
			}
			if ref.isTree() {
				// always save for tree test check
				c.Status = 1
				if err != nil {
					c.Coverage = nil
				}
			}
			err := c.Save()
			if err != nil {
				msg := fmt.Sprintf("Error: %v. Failed to save %v\n", err, c)
				outputSummary += msg
				LogError.Error(msg)
				_, _ = io.WriteString(log, msg)
			}
		}

		outputSummary += ("Test coverage: " + percentage + "\n")
		reportMessage = percentage
	} else if coveragePattern == "" && ref.isTree() {
		// saving build state with NULL coverage
		c := store.CommitsInfo{
			Owner:    ref.owner,
			Repo:     ref.repo,
			Sha:      ref.Sha,
			Author:   gpull.GetHead().GetUser().GetLogin(),
			Test:     testName,
			Coverage: nil,
			Passing:  0,
			Status:   1,
		}
		if conclusion == "success" {
			c.Passing = 1
		}
		err := c.Save()
		if err != nil {
			msg := fmt.Sprintf("Error: %v. Failed to save %v\n", err, c)
			LogError.Error(msg)
			_, _ = io.WriteString(log, msg)
		}
	}
	_, _ = io.WriteString(log, "\n")
	return
}
