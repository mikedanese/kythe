/*
 * Copyright 2016 Google Inc. All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package bazel implements the internal plumbing of a Bazel extractor for Go.
package bazel

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"bitbucket.org/creachadair/shell"
	"github.com/golang/protobuf/proto"
	"golang.org/x/net/context"

	"kythe.io/kythe/go/extractors/govname"
	"kythe.io/kythe/go/platform/kindex"
	"kythe.io/kythe/go/util/ptypes"
	"kythe.io/kythe/go/util/vnameutil"

	apb "kythe.io/kythe/proto/analysis_proto"
	bipb "kythe.io/kythe/proto/buildinfo_proto"
	spb "kythe.io/kythe/proto/storage_proto"
	eapb "kythe.io/third_party/bazel/extra_actions_base_proto"
)

// TODO(fromberger): The extractor logic depends on details of the Bazel rule
// implementation, which needs some cleanup.

func osOpen(_ context.Context, path string) (io.ReadCloser, error) { return os.Open(path) }

// A Config carries settings that control the extraction process.
type Config struct {
	Corpus   string          // the default corpus label to use
	Mnemonic string          // the build mnemonic to match (if "", matches all)
	Rules    vnameutil.Rules // rules for rewriting file VNames

	// If set, this function is used to open files for reading.  If nil,
	// os.Open is used.
	OpenRead func(context.Context, string) (io.ReadCloser, error)
}

// Extract extracts a compilation from the specified extra action info.
func (c *Config) Extract(ctx context.Context, info *eapb.ExtraActionInfo) (*kindex.Compilation, error) {
	si, err := proto.GetExtension(info, eapb.E_SpawnInfo_SpawnInfo)
	if err != nil {
		return nil, fmt.Errorf("extra action does not have SpawnInfo: %v", err)
	}
	spawnInfo := si.(*eapb.SpawnInfo)

	// Verify that the mnemonic is what we expect.
	if m := info.GetMnemonic(); m != c.Mnemonic && c.Mnemonic != "" {
		return nil, fmt.Errorf("mnemonic does not match %q ≠ %q", m, c.Mnemonic)
	}

	// Construct the basic compilation.
	toolArgs, err := c.extractToolArgs(ctx, spawnInfo.Argument)
	if err != nil {
		return nil, fmt.Errorf("extracting tool arguments: %v", err)
	}

	if len(toolArgs.sources) != 0 {
		if vname, ok := c.Rules.Apply(strings.TrimPrefix(toolArgs.sources[0], "k8s.io/kubernetes/")); ok {
			fmt.Println(vname.Corpus)
			c.Corpus = vname.Corpus
		}
	}

	cu := &kindex.Compilation{
		Proto: &apb.CompilationUnit{
			VName: &spb.VName{
				Language:  govname.Language,
				Corpus:    c.Corpus,
				Signature: info.GetOwner(),
			},
			Argument:         toolArgs.compile,
			SourceFile:       toolArgs.sources,
			WorkingDirectory: toolArgs.workDir,
			Environment: []*apb.CompilationUnit_Env{{
				Name:  "GOROOT",
				Value: toolArgs.goRoot,
			}},
		},
	}
	if info, err := ptypes.MarshalAny(&bipb.BuildDetails{
		BuildTarget: info.GetOwner(),
	}); err == nil {
		cu.Proto.Details = append(cu.Proto.Details, info)
	}

	// Load and populate file contents and required inputs.  First scan the
	// inputs and filter out which ones we actually want to keep by path
	// inspection; then load the contents concurrently.
	var wantPaths []string
	for _, in := range spawnInfo.InputFile {
		if toolArgs.wantInput(in) {
			wantPaths = append(wantPaths, in)
			cu.Files = append(cu.Files, nil)
			cu.Proto.RequiredInput = append(cu.Proto.RequiredInput, nil)
		}
	}

	// Fetch concurrently. Each element of the proto slices is accessed by a
	// single goroutine corresponding to its index.
	var wg sync.WaitGroup
	for i, path := range wantPaths {
		i, path := i, path
		wg.Add(1)
		go func() {
			defer wg.Done()
			fd, err := c.readFileData(ctx, path)
			if err != nil {
				log.Fatalf("Unable to read input %q: %v", path, err)
			}
			cu.Files[i] = fd
			cu.Proto.RequiredInput[i] = c.fileDataToInfo(fd)
		}()
	}
	wg.Wait()

	// Set the output path.  Although the SpawnInfo has room for multiple
	// outputs, we expect only one to be set in practice.  It's harmless if
	// there are more, though, so don't fail for that.
	for _, out := range spawnInfo.OutputFile {
		cu.Proto.OutputKey = out
		break
	}

	// Capture environment variables.
	for _, evar := range spawnInfo.Variable {
		if evar.GetName() == "PATH" {
			// TODO(fromberger): Perhaps whitelist or blacklist which
			// environment variables to capture here.
			continue
		}
		cu.Proto.Environment = append(cu.Proto.Environment, &apb.CompilationUnit_Env{
			Name:  evar.GetName(),
			Value: evar.GetValue(),
		})
	}

	return cu, nil
}

// openFile opens a the file at path using the opener from c or osOpen.
func (c *Config) openFile(ctx context.Context, path string) (io.ReadCloser, error) {
	open := c.OpenRead
	if open == nil {
		open = osOpen
	}
	return open(ctx, path)
}

// readFileData fetches the contents of the file at path and returns a FileData
// message populated with its content, path, and digest.
func (c *Config) readFileData(ctx context.Context, path string) (*apb.FileData, error) {
	f, err := c.openFile(ctx, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return kindex.FileData(path, f)
}

// fileDataToInfo produces a file info message corresponding to fd, using rules
// to generate the vname and root as the working directory.
func (c *Config) fileDataToInfo(fd *apb.FileData) *apb.CompilationUnit_FileInput {
	path := fd.Info.Path
	vname, ok := c.Rules.Apply(path)
	if !ok {
		vname = &spb.VName{Corpus: c.Corpus, Path: path}
	}
	return &apb.CompilationUnit_FileInput{
		VName: vname,
		Info:  fd.Info,
	}
}

// toolArgs captures the settings expressed by the Go compiler tool and its
// arguments.
type toolArgs struct {
	compile     []string // compiler argument list
	paramsFile  string   // the response file, if one was used
	workDir     string   // the compiler's working directory
	goRoot      string   // the GOROOT path
	importPath  string   // the import path being compiled
	includePath string   // an include path, if set
	outputPath  string   // the output from the compiler
	toolRoot    string   // root directory for compiler/libraries
	useCgo      bool     // whether cgo is enabled
	useRace     bool     // whether the race-detector is enabled
	sources     []string // source file paths
}

// wantInput reports whether path should be included as a required input.
func (g *toolArgs) wantInput(path string) bool {
	// Drop the response file (if there is one).
	if path == g.paramsFile {
		return false
	}

	// Otherwise, anything that isn't in the tool root we keep.
	trimmed, err := filepath.Rel(g.toolRoot, path)
	if err != nil || trimmed == path {
		return true
	}

	// Within the tool root, we keep library inputs, but discard binaries.
	// Filter libraries based on the race-detector settings.
	prefix, tail := splitPrefix(trimmed)
	switch prefix {
	case "bin/":
		return false
	case "pkg/":
		sub, _ := splitPrefix(tail)
		if strings.HasSuffix(sub, "_race/") && !g.useRace {
			return false
		}
		return sub != "tool/"
	default:
		return true // conservative fallback
	}
}

// bazelArgs captures compiler settings extracted from a Bazel response file.
type bazelArgs struct {
	paramsFile string   // the path of the params file (if there was one)
	goRoot     string   // the corpus-relative path of the Go root
	workDir    string   // the corpus-relative working directory
	compile    []string // the compiler argument list
}

var rootVar = regexp.MustCompile(`export +GOROOT=(?:\$\(pwd\)/)?(.+)$`)
var cdCommand = regexp.MustCompile(`cd +(.+)$`)

// parseBazelArgs extracts the compiler command line from the raw argument list
// passed in by Bazel. The official Go rules currently pass in a response file
// containing a shell script that we have to parse.
func (c *Config) parseBazelArgs(ctx context.Context, args []string) (*bazelArgs, error) {
	if len(args) != 1 || filepath.Ext(args[0]) != ".params" {
		// This is some unusual case; assume the arguments are already parsed.
		return &bazelArgs{compile: args}, nil
	}

	// This is the expected case, a response file.
	result := &bazelArgs{paramsFile: args[0]}
	f, err := c.openFile(ctx, result.paramsFile)
	if err != nil {
		return nil, err
	}
	data, err := ioutil.ReadAll(f)
	f.Close()
	if err != nil {
		return nil, err
	}

	// Split up the response into lines, and split each line into commands
	// assuming a pipeline of the form "cmd1 && cmd2 && ...".
	// Bazel exports GOROOT and changes the working directory, both of which we
	// want for processing the compiler's argument list.
	var last []string
	for _, line := range strings.Split(string(data), "\n") {
		for _, cmd := range strings.Split(line, "&&") {
			trimmed := strings.TrimSpace(cmd)
			last, _ = shell.Split(trimmed)
			if m := rootVar.FindStringSubmatch(trimmed); m != nil {
				result.goRoot = m[1]
			} else if m := cdCommand.FindStringSubmatch(trimmed); m != nil {
				result.workDir = m[1]
			}
		}
	}
	result.compile = last
	return result, nil
}

// extractToolArgs extracts the build tool arguments from args.
func (c *Config) extractToolArgs(ctx context.Context, args []string) (*toolArgs, error) {
	parsed, err := c.parseBazelArgs(ctx, args)
	if err != nil {
		return nil, err
	}

	result := &toolArgs{
		paramsFile: parsed.paramsFile,
		workDir:    parsed.workDir,
		goRoot:     filepath.Join(parsed.workDir, parsed.goRoot),
	}

	// Process the parsed command-line arguments to find the tool, source, and
	// output paths.
	var wantArg *string
	inTool := false
	for _, arg := range parsed.compile {
		// Discard arguments until the tool binary is found.
		if !inTool {
			if filepath.Base(arg) == "go" {
				adjusted := filepath.Join(result.workDir, arg)
				result.toolRoot = filepath.Dir(filepath.Dir(adjusted))
				result.compile = append(result.compile, adjusted)
				inTool = true
			}
			continue
		}

		// Scan for important flags.
		if wantArg != nil { // capture argument for a previous flag
			*wantArg = filepath.Join(parsed.workDir, arg)
			result.compile = append(result.compile, *wantArg)
			wantArg = nil
			continue
		}
		result.compile = append(result.compile, arg)
		if arg == "-p" {
			wantArg = &result.importPath
		} else if arg == "-o" {
			wantArg = &result.outputPath
		} else if arg == "-I" {
			wantArg = &result.includePath
		} else if arg == "-race" {
			result.useRace = true
		} else if !strings.HasPrefix(arg, "-") && strings.HasSuffix(arg, ".go") {
			result.sources = append(result.sources, arg)
		}
	}
	return result, nil
}

// splitPrefix separates the first slash-delimited component of path.
// The prefix includes the slash, so that prefix + tail == path.
// If there is no slash in the path, prefix == "".
func splitPrefix(path string) (prefix, tail string) {
	if i := strings.Index(path, "/"); i >= 0 {
		return path[:i+1], path[i+1:]
	}
	return "", path
}
