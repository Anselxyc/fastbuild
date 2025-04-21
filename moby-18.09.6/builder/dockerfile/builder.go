package dockerfile // import "github.com/docker/docker/builder/dockerfile"

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"sort"
	"strings"
	"time"

	"github.com/containerd/containerd/platforms"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/backend"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/fscache"
	"github.com/docker/docker/builder/remotecontext"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/image"
	"github.com/docker/docker/pkg/idtools"
	"github.com/docker/docker/pkg/streamformatter"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/docker/pkg/system"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
	"github.com/moby/buildkit/session"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/syncmap"
)

var validCommitCommands = map[string]bool{
	"cmd":         true,
	"entrypoint":  true,
	"healthcheck": true,
	"env":         true,
	"expose":      true,
	"label":       true,
	"onbuild":     true,
	"user":        true,
	"volume":      true,
	"workdir":     true,
}

const (
	stepFormat = "Step %d/%d : %v"
)

// SessionGetter is object used to get access to a session by uuid
type SessionGetter interface {
	Get(ctx context.Context, uuid string) (session.Caller, error)
}

// BuildManager is shared across all Builder objects
type BuildManager struct {
	idMapping *idtools.IdentityMapping
	backend   builder.Backend
	pathCache pathCache // TODO: make this persistent
	sg        SessionGetter
	fsCache   *fscache.FSCache
}

// NewBuildManager creates a BuildManager
func NewBuildManager(b builder.Backend, sg SessionGetter, fsCache *fscache.FSCache, identityMapping *idtools.IdentityMapping) (*BuildManager, error) {
	bm := &BuildManager{
		backend:   b,
		pathCache: &syncmap.Map{},
		sg:        sg,
		idMapping: identityMapping,
		fsCache:   fsCache,
	}
	if err := fsCache.RegisterTransport(remotecontext.ClientSessionRemote, NewClientSessionTransport()); err != nil {
		return nil, err
	}
	return bm, nil
}

// Build starts a new build from a BuildConfig
func (bm *BuildManager) Build(ctx context.Context, config backend.BuildConfig) (*builder.Result, error) {
	buildsTriggered.Inc()
	if config.Options.Dockerfile == "" {
		config.Options.Dockerfile = builder.DefaultDockerfileName
	}

	source, dockerfile, err := remotecontext.Detect(config)
	if err != nil {
		return nil, err
	}
	defer func() {
		if source != nil {
			if err := source.Close(); err != nil {
				logrus.Debugf("[BUILDER] failed to remove temporary context: %v", err)
			}
		}
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if src, err := bm.initializeClientSession(ctx, cancel, config.Options); err != nil {
		return nil, err
	} else if src != nil {
		source = src
	}

	builderOptions := builderOptions{
		Options:        config.Options,
		ProgressWriter: config.ProgressWriter,
		Backend:        bm.backend,
		PathCache:      bm.pathCache,
		IDMapping:      bm.idMapping,
	}
	b, err := newBuilder(ctx, builderOptions)
	if err != nil {
		return nil, err
	}
	return b.build(source, dockerfile)
}

func (bm *BuildManager) initializeClientSession(ctx context.Context, cancel func(), options *types.ImageBuildOptions) (builder.Source, error) {
	if options.SessionID == "" || bm.sg == nil {
		return nil, nil
	}
	logrus.Debug("client is session enabled")

	connectCtx, cancelCtx := context.WithTimeout(ctx, sessionConnectTimeout)
	defer cancelCtx()

	c, err := bm.sg.Get(connectCtx, options.SessionID)
	if err != nil {
		return nil, err
	}
	go func() {
		<-c.Context().Done()
		cancel()
	}()
	if options.RemoteContext == remotecontext.ClientSessionRemote {
		st := time.Now()
		csi, err := NewClientSessionSourceIdentifier(ctx, bm.sg, options.SessionID)
		if err != nil {
			return nil, err
		}
		src, err := bm.fsCache.SyncFrom(ctx, csi)
		if err != nil {
			return nil, err
		}
		logrus.Debugf("sync-time: %v", time.Since(st))
		return src, nil
	}
	return nil, nil
}

// builderOptions are the dependencies required by the builder
type builderOptions struct {
	Options        *types.ImageBuildOptions
	Backend        builder.Backend
	ProgressWriter backend.ProgressWriter
	PathCache      pathCache
	IDMapping      *idtools.IdentityMapping
}

// Builder is a Dockerfile builder
// It implements the builder.Backend interface.
type Builder struct {
	options *types.ImageBuildOptions

	Stdout io.Writer
	Stderr io.Writer
	Aux    *streamformatter.AuxFormatter
	Output io.Writer

	docker    builder.Backend
	clientCtx context.Context

	idMapping        *idtools.IdentityMapping
	disableCommit    bool
	imageSources     *imageSources
	pathCache        pathCache
	containerManager *containerManager
	imageProber      ImageProber
	platform         *specs.Platform
}

// newBuilder creates a new Dockerfile builder from an optional dockerfile and a Options.
func newBuilder(clientCtx context.Context, options builderOptions) (*Builder, error) {
	config := options.Options
	if config == nil {
		config = new(types.ImageBuildOptions)
	}

	b := &Builder{
		clientCtx:        clientCtx,
		options:          config,
		Stdout:           options.ProgressWriter.StdoutFormatter,
		Stderr:           options.ProgressWriter.StderrFormatter,
		Aux:              options.ProgressWriter.AuxFormatter,
		Output:           options.ProgressWriter.Output,
		docker:           options.Backend,
		idMapping:        options.IDMapping,
		imageSources:     newImageSources(clientCtx, options),
		pathCache:        options.PathCache,
		imageProber:      newImageProber(options.Backend, config.CacheFrom, config.NoCache),
		containerManager: newContainerManager(options.Backend),
	}

	// same as in Builder.Build in builder/builder-next/builder.go
	// TODO: remove once config.Platform is of type specs.Platform
	if config.Platform != "" {
		sp, err := platforms.Parse(config.Platform)
		if err != nil {
			return nil, err
		}
		if err := system.ValidatePlatform(sp); err != nil {
			return nil, err
		}
		b.platform = &sp
	}

	return b, nil
}

// Build 'LABEL' command(s) from '--label' options and add to the last stage
func buildLabelOptions(labels map[string]string, stages []instructions.Stage) {
	keys := []string{}
	for key := range labels {
		keys = append(keys, key)
	}

	// Sort the label to have a repeatable order
	sort.Strings(keys)
	for _, key := range keys {
		value := labels[key]
		stages[len(stages)-1].AddCommand(instructions.NewLabelCommand(key, value, true))
	}
}

// Build runs the Dockerfile builder by parsing the Dockerfile and executing
// the instructions from the file.
func (b *Builder) build(source builder.Source, dockerfile *parser.Result) (*builder.Result, error) {
	defer b.imageSources.Unmount()

	stages, metaArgs, err := instructions.Parse(dockerfile.AST)
	if err != nil {
		if instructions.IsUnknownInstruction(err) {
			buildsFailed.WithValues(metricsUnknownInstructionError).Inc()
		}
		return nil, errdefs.InvalidParameter(err)
	}
	if b.options.Target != "" {
		targetIx, found := instructions.HasStage(stages, b.options.Target)
		if !found {
			buildsFailed.WithValues(metricsBuildTargetNotReachableError).Inc()
			return nil, errdefs.InvalidParameter(errors.Errorf("failed to reach build target %s in Dockerfile", b.options.Target))
		}
		stages = stages[:targetIx+1]
	}

	// Add 'LABEL' command specified by '--label' option to the last stage
	buildLabelOptions(b.options.Labels, stages)

	dockerfile.PrintWarnings(b.Stderr)
	start := time.Now()
	// dispatchState, err := b.dispatchDockerfileWithCancellation(stages, metaArgs, dockerfile.EscapeToken, source)
	dispatchState, err := b.dispatchDockerfileWithCancellationInParallel(stages, metaArgs, dockerfile.EscapeToken, source)
	fmt.Fprintf(b.Stdout, "Build total time: %v s", time.Now().Sub(start).Seconds())
	fmt.Fprintln(b.Stdout)

	if err != nil {
		return nil, err
	}
	if dispatchState.imageID == "" {
		buildsFailed.WithValues(metricsDockerfileEmptyError).Inc()
		return nil, errors.New("No image was generated. Is your Dockerfile empty?")
	}
	return &builder.Result{ImageID: dispatchState.imageID, FromImage: dispatchState.baseImage}, nil
}

func emitImageID(aux *streamformatter.AuxFormatter, state *dispatchState) error {
	if aux == nil || state.imageID == "" {
		return nil
	}
	return aux.Emit("", types.BuildResult{ID: state.imageID})
}

func processMetaArg(meta instructions.ArgCommand, shlex *shell.Lex, args *BuildArgs) error {
	// shell.Lex currently only support the concatenated string format
	envs := convertMapToEnvList(args.GetAllAllowed())
	if err := meta.Expand(func(word string) (string, error) {
		return shlex.ProcessWord(word, envs)
	}); err != nil {
		return err
	}
	args.AddArg(meta.Key, meta.Value)
	args.AddMetaArg(meta.Key, meta.Value)
	return nil
}

func printCommand(out io.Writer, currentCommandIndex int, totalCommands int, cmd interface{}) int {
	fmt.Fprintf(out, stepFormat, currentCommandIndex, totalCommands, cmd)
	fmt.Fprintln(out)
	return currentCommandIndex + 1
}

func (b *Builder) dispatchDockerfileWithCancellation(parseResult []instructions.Stage, metaArgs []instructions.ArgCommand, escapeToken rune, source builder.Source) (*dispatchState, error) {
	dispatchRequest := dispatchRequest{}
	buildArgs := NewBuildArgs(b.options.BuildArgs)
	totalCommands := len(metaArgs) + len(parseResult)
	currentCommandIndex := 1
	for _, stage := range parseResult {
		totalCommands += len(stage.Commands)
	}
	shlex := shell.NewLex(escapeToken)
	for _, meta := range metaArgs {
		currentCommandIndex = printCommand(b.Stdout, currentCommandIndex, totalCommands, &meta)

		err := processMetaArg(meta, shlex, buildArgs)
		if err != nil {
			return nil, err
		}
	}

	stagesResults := newStagesBuildResults()

	for _, stage := range parseResult {
		if err := stagesResults.checkStageNameAvailable(stage.Name); err != nil {
			return nil, err
		}
		dispatchRequest = newDispatchRequest(b, escapeToken, source, buildArgs, stagesResults)

		currentCommandIndex = printCommand(b.Stdout, currentCommandIndex, totalCommands, stage.SourceCode)
		if err := initializeStage(dispatchRequest, &stage); err != nil {
			return nil, err
		}
		dispatchRequest.state.updateRunConfig()
		fmt.Fprintf(b.Stdout, " ---> %s\n", stringid.TruncateID(dispatchRequest.state.imageID))
		for _, cmd := range stage.Commands {
			select {
			case <-b.clientCtx.Done():
				logrus.Debug("Builder: build cancelled!")
				fmt.Fprint(b.Stdout, "Build cancelled\n")
				buildsFailed.WithValues(metricsBuildCanceled).Inc()
				return nil, errors.New("Build cancelled")
			default:
				// Not cancelled yet, keep going...
			}

			currentCommandIndex = printCommand(b.Stdout, currentCommandIndex, totalCommands, cmd)

			if err := dispatch(dispatchRequest, cmd, nil); err != nil {
				return nil, err
			}
			dispatchRequest.state.updateRunConfig()
			fmt.Fprintf(b.Stdout, " ---> %s\n", stringid.TruncateID(dispatchRequest.state.imageID))

		}
		if err := emitImageID(b.Aux, dispatchRequest.state); err != nil {
			return nil, err
		}
		buildArgs.MergeReferencedArgs(dispatchRequest.state.buildArgs)
		if err := commitStage(dispatchRequest.state, stagesResults); err != nil {
			return nil, err
		}
	}
	buildArgs.WarnOnUnusedBuildArgs(b.Stdout)
	return dispatchRequest.state, nil
}

func (b *Builder) dispatchDockerfileWithCancellationInParallel(parseResult []instructions.Stage, metaArgs []instructions.ArgCommand, escapeToken rune, source builder.Source) (*dispatchState, error) {
	dispatchRequest := dispatchRequest{}
	buildArgs := NewBuildArgs(b.options.BuildArgs)
	totalCommands := len(metaArgs) + len(parseResult)
	currentCommandIndex := 1
	for _, stage := range parseResult {
		totalCommands += len(stage.Commands)
	}
	shlex := shell.NewLex(escapeToken)
	for _, meta := range metaArgs {
		currentCommandIndex = printCommand(b.Stdout, currentCommandIndex, totalCommands, &meta)

		err := processMetaArg(meta, shlex, buildArgs)
		if err != nil {
			return nil, err
		}
	}

	commitConfigs := make(chan backend.CommitConfig, 128)
	runFinish := make(chan struct{})
	commitFinish := make(chan error)

	var idx []int64
	var states []*dispatchState
	var images []string
	var rwLayerNames []string

	var err error
	stagesResults := newStagesBuildResults()

	go func() {
		var runExit int = 0
		var exitFlag bool = false
		var commitError error
		var imageID image.ID
		var e error
		var containerOS string

		for {
			select {
			case cfg := <-commitConfigs:
				if cfg.Config == nil {
					images = append(images, "")
					continue
				}
				containerOS = cfg.ContainerOS
				if images[len(images)-1] != "" {
					cfg.ParentImageID = images[len(images)-1]
				}
				if cfg.ContainerMountLabel == "### PerformCopy Action ###" {
					rwLayerNames = append(rwLayerNames, cfg.ContainerID)
					cfg.ContainerMountLabel = ""
					imageID, e = b.exportConfusedImage(cfg)
				} else {
					imageID, e = b.docker.CommitBuildStep(cfg, false)
				}
				if e == nil {
					images = append(images, string(imageID))
				} else {
					commitError = e
					exitFlag = true
				}
			case <-runFinish:
				if err != nil { // dispatch completed with error
					runExit = -1
					exitFlag = true
				} else { // dispatch completed without error
					runExit = 1
				}
			default:
				if runExit == 1 && len(commitConfigs) == 0 {
					exitFlag = true
				}
			}
			if exitFlag {
				break
			}
		}
		// garbage collection
		// remove intermediate containers
		if b.options.ForceRemove || b.options.Remove {
			b.containerManager.RemoveAll(b.Stdout)
		}
		// release rwlayers created by COPY/ADD cmd
		if containerOS != "" {
			b.docker.ReleaseRWLayers(rwLayerNames, containerOS)
		}
		// commit stages
		for k := 0; k < len(idx); k++ {
			i := idx[k]
			if int64(len(images)) <= i {
				break
			}
			state := states[k]
			state.imageID = images[i]
			if e = emitImageID(b.Aux, state); e != nil {
				if commitError != nil {
					logrus.Errorf("emitImageID has en error: ", e)
				} else {
					commitError = e
				}
				break
			}
			if e = forcedCommitStage(state, stagesResults); e != nil {
				if commitError != nil {
					logrus.Errorf("commitStage has en error: ", e)
				} else {
					commitError = e
				}
				break
			}
		}
		commitFinish <- commitError
	}()

	var runCmds int64 = 0
	var confusedImages []image.ID

	for _, stage := range parseResult {
		if err = stagesResults.checkStageNameAvailable(stage.Name); err != nil {
			goto complete
		}
		dispatchRequest = newDispatchRequest(b, escapeToken, source, buildArgs, stagesResults)

		currentCommandIndex = printCommand(b.Stdout, currentCommandIndex, totalCommands, stage.SourceCode)
		if err = initializeStage(dispatchRequest, &stage); err != nil {
			goto complete
		}
		dispatchRequest.state.updateRunConfig()
		fmt.Fprintf(b.Stdout, " ---> %s\n", stringid.TruncateID(dispatchRequest.state.imageID))
		if len(stage.Commands) > 0 {
			commitConfigs <- backend.CommitConfig{}
		}
		for _, cmd := range stage.Commands {
			select {
			case <-b.clientCtx.Done():
				logrus.Debug("Builder: build cancelled!")
				fmt.Fprint(b.Stdout, "Build cancelled\n")
				buildsFailed.WithValues(metricsBuildCanceled).Inc()
				err = errors.New("Build cancelled")
				goto complete
			default:
				// Not cancelled yet, keep going...
			}

			currentCommandIndex = printCommand(b.Stdout, currentCommandIndex, totalCommands, cmd)

			if err = dispatch(dispatchRequest, cmd, commitConfigs); err != nil {
				goto complete
			}
			dispatchRequest.state.updateRunConfig()
			fmt.Fprintf(b.Stdout, " ---> %s\n", stringid.TruncateID(dispatchRequest.state.imageID))
			confusedImages = append(confusedImages, image.ID(dispatchRequest.state.imageID))
		}
		if err = emitImageID(b.Aux, dispatchRequest.state); err != nil {
			goto complete
		}
		buildArgs.MergeReferencedArgs(dispatchRequest.state.buildArgs)
		if err = commitStage(dispatchRequest.state, stagesResults); err != nil {
			goto complete
		}
		if len(stage.Commands) > 0 {
			runCmds += int64(len(stage.Commands))
			states = append(states, dispatchRequest.state)
			idx = append(idx, runCmds)
		}
	}
	buildArgs.WarnOnUnusedBuildArgs(b.Stdout)

complete:
	runFinish <- struct{}{}
	e := <-commitFinish

	b.docker.DeleteConfusedImages(confusedImages)
	if len(images) != 0 {
		dispatchRequest.state.imageID = images[len(images)-1]
	}

	if err != nil {
		return nil, err
	}
	if e != nil {
		return nil, e
	}

	return dispatchRequest.state, nil
}

// BuildFromConfig builds directly from `changes`, treating it as if it were the contents of a Dockerfile
// It will:
// - Call parse.Parse() to get an AST root for the concatenated Dockerfile entries.
// - Do build by calling builder.dispatch() to call all entries' handling routines
//
// BuildFromConfig is used by the /commit endpoint, with the changes
// coming from the query parameter of the same name.
//
// TODO: Remove?
func BuildFromConfig(config *container.Config, changes []string, os string) (*container.Config, error) {
	if !system.IsOSSupported(os) {
		return nil, errdefs.InvalidParameter(system.ErrNotSupportedOperatingSystem)
	}
	if len(changes) == 0 {
		return config, nil
	}

	dockerfile, err := parser.Parse(bytes.NewBufferString(strings.Join(changes, "\n")))
	if err != nil {
		return nil, errdefs.InvalidParameter(err)
	}

	b, err := newBuilder(context.Background(), builderOptions{
		Options: &types.ImageBuildOptions{NoCache: true},
	})
	if err != nil {
		return nil, err
	}

	// ensure that the commands are valid
	for _, n := range dockerfile.AST.Children {
		if !validCommitCommands[n.Value] {
			return nil, errdefs.InvalidParameter(errors.Errorf("%s is not a valid change command", n.Value))
		}
	}

	b.Stdout = ioutil.Discard
	b.Stderr = ioutil.Discard
	b.disableCommit = true

	var commands []instructions.Command
	for _, n := range dockerfile.AST.Children {
		cmd, err := instructions.ParseCommand(n)
		if err != nil {
			return nil, errdefs.InvalidParameter(err)
		}
		commands = append(commands, cmd)
	}

	dispatchRequest := newDispatchRequest(b, dockerfile.EscapeToken, nil, NewBuildArgs(b.options.BuildArgs), newStagesBuildResults())
	// We make mutations to the configuration, ensure we have a copy
	dispatchRequest.state.runConfig = copyRunConfig(config)
	dispatchRequest.state.imageID = config.Image
	dispatchRequest.state.operatingSystem = os
	for _, cmd := range commands {
		err := dispatch(dispatchRequest, cmd, nil)
		if err != nil {
			return nil, errdefs.InvalidParameter(err)
		}
		dispatchRequest.state.updateRunConfig()
	}

	return dispatchRequest.state.runConfig, nil
}

func convertMapToEnvList(m map[string]string) []string {
	result := []string{}
	for k, v := range m {
		result = append(result, k+"="+v)
	}
	return result
}
