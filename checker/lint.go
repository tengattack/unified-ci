package checker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/martinlindhe/go-difflib/difflib"
	"github.com/sqs/goreturns/returns"
	"golang.org/x/lint"
	"golang.org/x/tools/imports"
	"sourcegraph.com/sourcegraph/go-diff/diff"
)

// A value in (0,1] estimating the confidence of correctness in golint reports
// This value is used internally by golint. Its default value is 0.8
const golintMinConfidenceDefault = 0.8
const (
	severityLevelOff = iota
	severityLevelWarning
	severityLevelError
)
const (
	ruleGolint            = "golint"
	ruleGoreturns         = "goreturns"
	ruleMarkdownFormatted = "remark"
)

// LintEnabled list enabled linter
type LintEnabled struct {
	CPP        bool
	Go         bool
	PHP        bool
	TypeScript bool
	SCSS       bool
	JS         string
	ES         string
	MD         bool
	APIDoc     bool
}

// LintMessage is a single lint message for PHPLint
type LintMessage struct {
	RuleID     string `json:"ruleId"`
	Severity   int    `json:"severity"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	Message    string `json:"message"`
	SourceCode string `json:"sourceCode,omitempty"`
}

// LintResult is a single lint result for PHPLint
type LintResult struct {
	FilePath string        `json:"filePath"`
	Messages []LintMessage `json:"messages"`
}

// TSLintResult is a single lint result for TSLint
type TSLintResult struct {
	Name          string         `json:"name"`
	RuleName      string         `json:"ruleName"`
	RuleSeverity  string         `json:"ruleSeverity"`
	Failure       string         `json:"failure"`
	StartPosition TSLintPosition `json:"startPosition"`
	EndPosition   TSLintPosition `json:"endPosition"`
}

// TSLintPosition is the source code position
type TSLintPosition struct {
	Character int `json:"character"`
	Line      int `json:"line"`
	Position  int `json:"position"`
}

// SCSSLintResult is a single lint result for SCSSLint
type SCSSLintResult struct {
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Length   int    `json:"length"`
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
	Linter   string `json:"linter"`
}

// LintSeverity is the map of rule severity name
var LintSeverity map[string]int

func init() {
	LintSeverity = map[string]int{
		"off":     severityLevelOff,
		"warning": severityLevelWarning,
		"error":   severityLevelError,
	}
}

// Init default LintEnabled struct
func (lintEnabled *LintEnabled) Init(cwd string) {

	// reset to defaults
	lintEnabled.CPP = false
	lintEnabled.Go = false
	lintEnabled.PHP = true
	lintEnabled.TypeScript = false
	lintEnabled.SCSS = false
	lintEnabled.JS = ""
	lintEnabled.ES = ""
	lintEnabled.MD = false
	lintEnabled.APIDoc = false

	if _, err := os.Stat(filepath.Join(cwd, ".golangci.yml")); err == nil {
		lintEnabled.Go = true
	}
	if _, err := os.Stat(filepath.Join(cwd, "CPPLINT.cfg")); err == nil {
		lintEnabled.CPP = true
	}
	if _, err := os.Stat(filepath.Join(cwd, ".remarkrc")); err == nil {
		lintEnabled.MD = true
	} else if _, err := os.Stat(filepath.Join(cwd, ".remarkrc.js")); err == nil {
		lintEnabled.MD = true
	}
	if _, err := os.Stat(filepath.Join(cwd, "tslint.json")); err == nil {
		lintEnabled.TypeScript = true
	}
	if _, err := os.Stat(filepath.Join(cwd, ".scss-lint.yml")); err == nil {
		lintEnabled.SCSS = true
	}
	if _, err := os.Stat(filepath.Join(cwd, ".eslintrc")); err == nil {
		lintEnabled.ES = filepath.Join(cwd, ".eslintrc")
	}
	if _, err := os.Stat(filepath.Join(cwd, ".eslintrc.js")); err == nil {
		lintEnabled.JS = filepath.Join(cwd, ".eslintrc.js")
		if lintEnabled.ES == "" {
			lintEnabled.ES = lintEnabled.JS
		}
	} else {
		lintEnabled.JS = lintEnabled.ES
	}
	if _, err := os.Stat(filepath.Join(cwd, "apidoc.json")); err == nil {
		lintEnabled.APIDoc = true
	}
}

// CPPLint lints the cpp language files using github.com/cpplint/cpplint
func CPPLint(filePath string, repoPath string) (lints []LintMessage, err error) {
	parser := NewShellParser(repoPath)
	words, err := parser.Parse(Conf.Core.CPPLint)
	if err != nil {
		LogError.Error("CPPLint: " + err.Error())
		return nil, err
	}
	words = append(words, "--quiet", filePath)
	cmd := exec.Command(words[0], words[1:]...)
	cmd.Dir = repoPath

	var output bytes.Buffer
	cmd.Stderr = &output

	// the exit status is not 0 when cpplint finds a problem in code files
	err = cmd.Run()
	if err != nil && err.Error() != "exit status 1" {
		LogError.Error("CPPLint: " + err.Error())
		return nil, err
	}
	outputStr := output.String()
	LogAccess.Debugf("CPPLint Output:\n%s", outputStr)
	lines := strings.Split(outputStr, "\n")

	// Sample output: "code.cpp:138:  Missing spaces around =  [whitespace/operators] [4]"
	re := regexp.MustCompile(`:(\d+):(.+)\[(.+?)\] \[\d\]\s*$`)
	for _, line := range lines {
		matched := false
		lineNum := 0
		msg := ""
		rule := ""

		match := re.FindStringSubmatch(line)
		for i, m := range match {
			switch i {
			case 1:
				// line number
				lineNum, _ = strconv.Atoi(m)
			case 2:
				// warning message
				msg = m
			case 3:
				rule = m
				matched = true
			}
		}
		if matched {
			lints = append(lints, LintMessage{
				RuleID:   rule,
				Severity: severityLevelError,
				Line:     lineNum,
				Column:   0,
				Message:  msg,
			})
		}
	}
	return lints, nil
}

// PHPLint lints the php files
func PHPLint(fileName, cwd string) ([]LintMessage, string, error) {
	var stderr bytes.Buffer

	parser := NewShellParser(cwd)
	words, err := parser.Parse(Conf.Core.PHPLint)
	if err != nil {
		LogError.Error("PHPLint: " + err.Error())
		return nil, stderr.String(), err
	}
	words = append(words, "-f", "json", fileName)
	cmd := exec.Command(words[0], words[1:]...)
	cmd.Stderr = &stderr
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil, stderr.String(), err
	}

	LogAccess.Debugf("PHPLint Output:\n%s", out)

	var results []LintResult
	err = json.Unmarshal(out, &results)
	if err != nil {
		return nil, stderr.String(), err
	}

	if len(results) <= 0 {
		return []LintMessage{}, stderr.String(), nil
	}
	return results[0].Messages, stderr.String(), nil
}

// ESLint lints the js, jsx, es, esx files
func ESLint(fileName, cwd, eslintrc string) ([]LintMessage, string, error) {
	var stderr bytes.Buffer

	parser := NewShellParser(cwd)
	words, err := parser.Parse(Conf.Core.ESLint)
	if err != nil {
		LogError.Error("ESLint: " + err.Error())
		return nil, stderr.String(), err
	}
	if eslintrc != "" {
		words = append(words, "-c", eslintrc, "-f", "json", fileName)
	} else {
		words = append(words, "-f", "json", fileName)
	}
	cmd := exec.Command(words[0], words[1:]...)
	cmd.Dir = cwd
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return nil, stderr.String(), err
		}
	}

	LogAccess.Debugf("ESLint Output:\n%s", out)

	var results []LintResult
	err = json.Unmarshal(out, &results)
	if err != nil {
		return nil, stderr.String(), err
	}

	if len(results) <= 0 {
		return []LintMessage{}, stderr.String(), nil
	}
	return results[0].Messages, stderr.String(), nil
}

// TSLint lints the ts and tsx files
func TSLint(fileName, cwd string) ([]LintMessage, string, error) {
	var stderr bytes.Buffer

	parser := NewShellParser(cwd)
	words, err := parser.Parse(Conf.Core.TSLint)
	if err != nil {
		LogError.Error("TSLint: " + err.Error())
		return nil, stderr.String(), err
	}
	words = append(words, "--format", "json", fileName)
	cmd := exec.Command(words[0], words[1:]...)
	cmd.Dir = cwd
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return nil, stderr.String(), err
		}
	}

	LogAccess.Debugf("TSLint Output:\n%s", out)

	var results []TSLintResult
	err = json.Unmarshal(out, &results)
	if err != nil {
		return nil, stderr.String(), err
	}

	if len(results) <= 0 {
		return []LintMessage{}, stderr.String(), nil
	}

	var messages []LintMessage
	messages = make([]LintMessage, len(results))
	for i, lint := range results {
		ruleSeverity := strings.ToLower(lint.RuleSeverity)
		level, ok := LintSeverity[ruleSeverity]
		if !ok {
			level = severityLevelOff
		}
		messages[i] = LintMessage{
			RuleID:   lint.RuleName,
			Severity: level,
			Line:     lint.StartPosition.Line + 1,
			Column:   lint.StartPosition.Character + 1,
			Message:  lint.Failure,
		}
	}
	return messages, stderr.String(), nil
}

// SCSSLint lints the scss files
func SCSSLint(fileName, cwd string) ([]LintMessage, string, error) {
	var stderr bytes.Buffer

	parser := NewShellParser(cwd)
	words, err := parser.Parse(Conf.Core.SCSSLint)
	if err != nil {
		LogError.Error("SCSSLint: " + err.Error())
		return nil, stderr.String(), err
	}
	words = append(words, "--format=JSON", fileName)
	cmd := exec.Command(words[0], words[1:]...)
	cmd.Dir = cwd
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return nil, stderr.String(), err
		}
	}

	LogAccess.Debugf("SCSSLint Output:\n%s", out)

	var results map[string][]SCSSLintResult
	err = json.Unmarshal(out, &results)
	if err != nil {
		return nil, stderr.String(), err
	}

	if len(results) <= 0 {
		return []LintMessage{}, stderr.String(), nil
	}

	var messages []LintMessage
	for _, lints := range results {
		messages = make([]LintMessage, len(lints))
		for i, lint := range lints {
			ruleSeverity := strings.ToLower(lint.Severity)
			level, ok := LintSeverity[ruleSeverity]
			if !ok {
				level = severityLevelOff
			}
			messages[i] = LintMessage{
				RuleID:   lint.Linter,
				Severity: level,
				Line:     lint.Line,
				Column:   lint.Column,
				Message:  lint.Reason,
			}
		}
		break
	}
	return messages, stderr.String(), nil
}

// CodeClimate --out-format code-climate
type CodeClimate struct {
	Description string `json:"description"`
	Location    struct {
		Path  string `json:"path"`
		Lines struct {
			Begin int `json:"begin"`
		} `json:"lines"`
	} `json:"location"`
}

// GolangCILint runs `golangci-lint run --out-format code-climate`
func GolangCILint(ctx context.Context, cwd string) ([]CodeClimate, string, error) {
	parser := NewShellParser(cwd)
	words, err := parser.Parse(Conf.Core.GolangCILint)
	if err == nil && len(words) < 1 {
		err = errors.New("GolangCILint command is not configured")
	}
	words = append(words, "run", "--out-format", "code-climate")

	if err != nil {
		LogError.Error("GolangCILint: " + err.Error())
		return nil, "", err
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, words[0], words[1:]...)
	cmd.Stderr = &stderr
	cmd.Dir = cwd
	out, _ := cmd.Output()

	LogAccess.Debugf("GolangCILint Output:\n%s", out)

	var suggestions []CodeClimate
	err = json.Unmarshal(out, &suggestions)
	if err != nil {
		LogError.Error("GolangCILint: " + err.Error())
		return nil, stderr.String(), err
	}
	if runtime.GOOS == "windows" {
		for i, v := range suggestions {
			suggestions[i].Location.Path = filepath.ToSlash(v.Location.Path)
		}
	}
	return suggestions, stderr.String(), nil
}

// Goreturns formats the go code
func Goreturns(filePath, repoPath string) (lints []LintMessage, err error) {
	ruleID := ruleGoreturns
	fileDiff, err := goreturns(filePath)
	if err != nil {
		return nil, err
	}
	lints = getLintsFromDiff(fileDiff, lints, ruleID)
	return lints, nil
}

// Golint lints the go file
func Golint(filePath, repoPath string) (lints []LintMessage, err error) {
	ruleID := ruleGolint
	ps, err := golint(filePath)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Confidence >= golintMinConfidenceDefault {
			lints = append(lints, LintMessage{
				RuleID:   ruleID,
				Severity: severityLevelError,
				Line:     p.Position.Line,
				Column:   p.Position.Column,
				Message:  p.Text,
			})
		}
	}
	return lints, nil
}

func goreturns(filePath string) (*diff.FileDiff, error) {
	pkgDir := filepath.Dir(filePath)

	opt := &returns.Options{}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	src, err := ioutil.ReadAll(f)
	if err != nil {
		return nil, err
	}
	// src holds the original content.
	var res = src

	res, err = imports.Process(filePath, res, &imports.Options{
		Fragment:  opt.Fragment,
		AllErrors: opt.AllErrors,
		Comments:  true,
		TabIndent: true,
		TabWidth:  8,
	})
	if err != nil {
		return nil, err
	}

	res, err = returns.Process(pkgDir, filePath, res, opt)
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(src, res) {
		udf := difflib.UnifiedDiff{
			A:        difflib.SplitLines(string(src)),
			B:        difflib.SplitLines(string(res)),
			FromFile: "original",
			ToFile:   "formatted",
			Context:  0,
		}
		data, err := difflib.GetUnifiedDiffString(udf)
		if err != nil {
			return nil, fmt.Errorf("computing diff: %s", err)
		}
		if data == "" {
			// TODO: final EOL
			return nil, nil
		}
		return diff.ParseFileDiff([]byte(data))
	}
	return nil, nil
}

func golint(filePath string) ([]lint.Problem, error) {
	files := make(map[string][]byte)
	src, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	files[filePath] = src

	l := new(lint.Linter)
	ps, err := l.LintFiles(files)
	if err != nil {
		return nil, err
	}
	return ps, nil
}

// MDLint generates lint messages from the report of remark-lint
func MDLint(rps []remarkReport) (lints []LintMessage, err error) {
	for i, r := range rps {
		if i == 0 {
			for _, m := range r.Messages {
				lints = append(lints, LintMessage{
					RuleID:  m.RuleID,
					Line:    m.Line,
					Message: m.Reason,
				})
			}
		}
	}
	return lints, nil
}

type remarkReport struct {
	Messages []remarkMessage
}

type remarkMessage struct {
	Line   int
	Reason string
	RuleID string
}

func remark(fileName string, repoPath string) (reports []remarkReport, out []byte, err error) {
	parser := NewShellParser(repoPath)
	words, err := parser.Parse(Conf.Core.RemarkLint)
	if err != nil {
		LogError.Error("RemarkLint: " + err.Error())
		return nil, nil, err
	}
	words = append(words, "--quiet", "--report", "json", fileName)
	cmd := exec.Command(words[0], words[1:]...)
	cmd.Dir = repoPath
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}

	err = cmd.Start()
	if err != nil {
		return nil, nil, err
	}

	stdoutStr, err := ioutil.ReadAll(stdout)
	LogAccess.Debugf("RemarkLint Stdout:\n%s", stdoutStr)
	if err != nil {
		return nil, nil, err
	}
	stderrStr, err := ioutil.ReadAll(stderr)
	LogAccess.Debugf("RemarkLint Stderr:\n%s", stderrStr)
	if err != nil {
		return nil, stdoutStr, err
	}
	err = json.Unmarshal(stderrStr, &reports)
	if err != nil {
		return nil, stdoutStr, err
	}
	return reports, stdoutStr, cmd.Wait()
}

func markdownFormatted(filePath string, result []byte) (*diff.FileDiff, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	src, err := ioutil.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(src, result) {
		udf := difflib.UnifiedDiff{
			A:        difflib.SplitLines(string(src)),
			B:        difflib.SplitLines(string(result)),
			FromFile: "original",
			ToFile:   "formatted",
			Context:  0,
		}
		data, err := difflib.GetUnifiedDiffString(udf)
		if err != nil {
			return nil, fmt.Errorf("computing diff: %s", err)
		}
		if data == "" {
			// TODO: final EOL
			return nil, nil
		}
		return diff.ParseFileDiff([]byte(data))
	}
	return nil, nil
}

// MDFormattedLint generates lint messages from diffs of remark
func MDFormattedLint(filePath string, result []byte) (lints []LintMessage, err error) {
	ruleID := ruleMarkdownFormatted
	fileDiff, err := markdownFormatted(filePath, result)
	if err != nil {
		return nil, err
	}
	lints = getLintsFromDiff(fileDiff, lints, ruleID)
	return lints, nil
}

// APIDoc generates apidoc
func APIDoc(ctx context.Context, repoPath string) error {
	parser := NewShellParser(repoPath)
	words, err := parser.Parse(Conf.Core.APIDoc)
	if err != nil {
		LogError.Error("APIDoc: " + err.Error())
		return err
	}
	cmd := exec.Command(words[0], words[1:]...)
	cmd.Dir = repoPath
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	err = cmd.Start()
	if err != nil {
		return err
	}

	stdoutStr, err := ioutil.ReadAll(stdout)
	LogAccess.Debugf("APIDoc Stdout:\n%s", stdoutStr)
	if err != nil {
		return err
	}
	stderrStr, err := ioutil.ReadAll(stderr)
	LogAccess.Debugf("APIDoc Stderr:\n%s", stderrStr)
	if err != nil {
		return err
	}

	return cmd.Wait()
}
