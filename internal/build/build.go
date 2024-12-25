package build

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/frontend/gateway/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type BuildDelegate struct{}

func New() BuildDelegate {
	return BuildDelegate{}
}

// Builder contains the Build function we pass to buildkit
type Builder struct {
	ctx context.Context
	c   client.Client
	bc  *dockerui.Client
}

// Build runs the build
func (b BuildDelegate) Build(ctx context.Context, c client.Client) (*client.Result, error) {
	bc, err := dockerui.NewClient(c)
	if err != nil {
		return nil, err
	}
	builder := Builder{
		ctx: ctx,
		bc:  bc,
		c:   c,
	}
	return builder.BuildWithNix()
}

// mimic dockerfile.v1 frontend
const (
	localNameContext     = "context"
	localNameDockerfile  = "dockerfile"
	dockerignoreFilename = ".dockerignore"
	keyFilename          = "filename"
	keyTargetName        = "target"
)

type LayerResult struct {
	State llb.State
	Info  Layer
}

func (b *Builder) BuildWithNix() (*client.Result, error) {
	nixBuilder := GetBuilder()
	buildResult, err := b.Nix2ContainerBuild(nixBuilder)
	if err != nil {
		return nil, errors.Wrap(err, "failed to run nix2container build")
	}
	config, err := b.ParseNix2ContainerConfig(buildResult)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse nix2container config")
	}
	layersSt, err := b.ConstructLayers(nixBuilder, buildResult, *config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to construct layers")
	}
	finalResult, err := b.CombineLayers(layersSt)
	if err != nil {
		return nil, errors.Wrap(err, "failed to combine layers")
	}
	image, err := b.ExportImage(finalResult, *config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to export image")
	}
	return image, nil
}

func GetBuilder() llb.State {
	return llb.Image("nixos/nix:2.24.11").
		AddEnv("PATH", "/bin:/usr/bin:/nix/var/nix/profiles/default/bin").
		File(llb.Mkdir("/out", 0755, llb.WithParents(true)))
}

func GetSelfImage() llb.State {
	return llb.Image(os.Getenv("BUILDER_TAG"), llb.ResolveDigest(true))
}

func (b *Builder) Nix2ContainerBuild(nixBuilder llb.State) (*client.Result, error) {
	excludePatterns, err := b.bc.DockerIgnorePatterns(b.ctx)
	if err != nil {
		return nil, err
	}

	localCtxSt := llb.Local(localNameContext,
		llb.SessionID(b.c.BuildOpts().SessionID),
		llb.ExcludePatterns(excludePatterns),
		dockerui.WithInternalName("local context"),
	)

	targetName := b.c.BuildOpts().Opts[keyTargetName]
	flakePath := "."
	if targetName != "" {
		flakePath = fmt.Sprintf("%s#%s", flakePath, targetName)
	}

	buildSt := nixBuilder.Run(
		llb.AddMount("/context", localCtxSt),
		llb.AddMount("/nix", nixBuilder, llb.SourcePath("/nix"), llb.AsPersistentCacheDir("nix2container-buildkit-nix-cache", llb.CacheMountShared)),
		llb.Dir("/context"),
		llb.Shlexf("bash -c \"nix -L --extra-experimental-features 'nix-command flakes' build --max-jobs auto --accept-flake-config --option build-users-group '' -o /out/image-link.json %s && cat /out/image-link.json > /out/image.json\"", flakePath),
		llb.WithCustomNamef("running nix2container build"),
	)

	// Get build result
	def, err := buildSt.Marshal(b.ctx)
	if err != nil {
		return nil, err
	}

	buildResult, err := b.c.Solve(b.ctx, client.SolveRequest{
		Definition: def.ToPB(),
	})
	return buildResult, err
}

func (b *Builder) ParseNix2ContainerConfig(buildResult *client.Result) (config *Nix2ContainerConfig, err error) {
	// Read and parse the image config
	configBytes, err := buildResult.Ref.ReadFile(b.ctx, client.ReadRequest{
		Filename: "/out/image.json",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read image config: %w", err)
	}

	if err := json.Unmarshal(configBytes, &config); err != nil {
		return nil, fmt.Errorf("failed to parse image config: %w", err)
	}
	return config, nil
}

func (b *Builder) ConstructLayers(nixBuilder llb.State, buildResult *client.Result, config Nix2ContainerConfig) ([]LayerResult, error) {
	selfImageSt := GetSelfImage()

	// Create layersSt for each store path
	var layersSt []LayerResult
	for _, layer := range config.Layers {
		layerJsonBytes, err := json.Marshal(layer)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize layer: %w", err)
		}

		layerJsonState := llb.Scratch().
			File(llb.Mkfile("/layer.json", 0644, layerJsonBytes, llb.WithCreatedTime(time.Time{})),
				llb.WithCustomNamef("preparing manifest for layer %s", layer.Digest))

		// Construct layer by copying paths from cached nix store
		constructLayerExec := llb.Scratch().
			File(llb.Mkdir("/nix/store", 0755, llb.WithParents(true), llb.WithCreatedTime(time.Time{}))).
			Run(
				llb.AddMount("/src", nixBuilder, llb.AsPersistentCacheDir("nix2container-buildkit-nix-cache", llb.CacheMountShared)),
				llb.AddMount("/self", selfImageSt),
				llb.AddMount("/build", layerJsonState),
				llb.Shlexf("/self/construct-layer --config %s --source-prefix %s --dest-prefix %s", "/build/layer.json", "/src", "/"),
				llb.WithCustomNamef("constructing layer %s", layer.Digest),
			).Root()

		constructLayerExec = constructLayerExec.
			// Remove junk directories after script run, otherwise we lose reproducibility
			File(llb.Rm("/dev"), dockerui.WithInternalName(fmt.Sprintf("%s: cleaning up /dev", layer.Digest))).
			File(llb.Rm("/proc"), dockerui.WithInternalName(fmt.Sprintf("%s: cleaning up /proc", layer.Digest))).
			File(llb.Rm("/sys"), dockerui.WithInternalName(fmt.Sprintf("%s: cleaning up /sys", layer.Digest)))

		layersSt = append(layersSt, LayerResult{
			State: constructLayerExec.Reset(llb.Scratch()),
			Info:  layer,
		})
	}

	return layersSt, nil
}

func (b *Builder) CombineLayers(layers []LayerResult) (*client.Result, error) {
	var finalState llb.State
	if IsMergeSupported(b.c) {
		states := []llb.State{}
		for _, layer := range layers {
			states = append(states, layer.State)
		}
		finalState = llb.Merge(states)
	} else {
		// Create final state by copying layers
		finalState = llb.Scratch()
		for _, layer := range layers {
			finalState = finalState.File(
				llb.Copy(layer.State, "/", "/", &llb.CopyInfo{
					CopyDirContentsOnly: true,
				}),
				llb.WithCustomNamef("copy layer %s", layer.Info.Digest),
			)
		}
	}

	// Create final result
	finalDef, err := finalState.Marshal(b.ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal final state")
	}
	finalResult, err := b.c.Solve(b.ctx, client.SolveRequest{
		Definition: finalDef.ToPB(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to solve final state")
	}

	return finalResult, nil
}

func (b *Builder) ExportImage(finalResult *client.Result, config Nix2ContainerConfig) (*client.Result, error) {
	res := client.NewResult()
	ref, err := finalResult.SingleRef()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get single ref")
	}

	platform := platforms.DefaultSpec()

	// Create image configuration
	ImageConfig := config.ImageConfig
	image := specs.Image{
		Config: ImageConfig,
		RootFS: specs.RootFS{
			Type: "layers",
		},
		Platform: platform,
	}
	specJson, err := json.Marshal(image)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal image config")
	}
	res.SetRef(ref)
	res.AddMeta(exptypes.ExporterImageConfigKey, specJson)

	return res, nil
}
