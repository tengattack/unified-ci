package checker

import (
	"path"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIgnoreFilePath(t *testing.T) {
	assert := assert.New(t)

	assert.True(MatchAny([]string{"sdk/**"}, "sdk/v2/x"))
	assert.False(MatchAny([]string{"sdk/*"}, "sdk/v2/x"))
}

func TestReadProjectConfig(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	_, filename, _, _ := runtime.Caller(0)
	currentDir := path.Dir(filename)

	repoConf, err := readProjectConfig(currentDir)
	require.NoError(err)
	assert.Empty(repoConf.Tests)

	repoConf, err = readProjectConfig(currentDir + "/../")
	require.NoError(err)
	assert.True(len(repoConf.Tests) > 0)
	assert.Equal([]string{
		"testdata/**",
		"sdk/**",
	}, repoConf.IgnorePatterns)
}

func TestNewShellParser(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	_, filename, _, _ := runtime.Caller(0)
	currentDir := path.Dir(filename)

	parser := NewShellParser(currentDir)
	require.NotNil(parser)

	words, err := parser.Parse("echo $PWD $PROJECT_NAME")
	require.NoError(err)
	assert.Equal([]string{"echo", currentDir, "checker"}, words)
}
