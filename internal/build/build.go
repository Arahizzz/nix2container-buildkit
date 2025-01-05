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

type Builder struct {
	ctx context.Context
	c   client.Client
	bc  *dockerui.Client
}

// Buildkit Entrypoint
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

// Get nix builder
func GetBuilder() llb.State {
	return llb.Image("nixos/nix:2.24.11").
		AddEnv("PATH", "/bin:/usr/bin:/nix/var/nix/profiles/default/bin").
		File(llb.Mkdir("/out", 0755, llb.WithParents(true)))
}

// Recursively load this image to be able to mount and get access to util binaries
func GetSelfImage() llb.State {
	return llb.Image(os.Getenv("BUILDER_TAG"), llb.ResolveDigest(true))
}

// Run nix2container build with mounted nix store cache
func (b *Builder) Nix2ContainerBuild(nixBuilder llb.State) (*client.Result, error) {
	// Parse .dockerignore
	excludePatterns, err := b.bc.DockerIgnorePatterns(b.ctx)
	if err != nil {
		return nil, err
	}

	// Build context
	localCtxSt := llb.Local(localNameContext,
		llb.SessionID(b.c.BuildOpts().SessionID),
		llb.ExcludePatterns(excludePatterns),
		dockerui.WithInternalName("local context"),
	)

	// Check if flake target is specified
	targetName := b.c.BuildOpts().Opts[keyTargetName]
	flakePath := "."
	if targetName != "" {
		// Append target to flake path
		flakePath = fmt.Sprintf("%s#%s", flakePath, targetName)
	}

	// Build using nix2container
	buildSt := nixBuilder.Run(
		llb.AddMount("/context", localCtxSt),
		// Cache nix store to speed future rebuilds
		llb.AddMount("/nix", nixBuilder, llb.SourcePath("/nix"), llb.AsPersistentCacheDir("nix2container-buildkit-nix-cache", llb.CacheMountShared)),
		llb.Dir("/context"),
		llb.Shlexf("bash -c \"nix -L --extra-experimental-features 'nix-command flakes'" + 
			" build --max-jobs auto --accept-flake-config --option build-users-group '' -o /out/image-link.json %s" +
			// Read symlink into actual file, otherwise unreadable without mounted cache
			" && cat /out/image-link.json > /out/image.json\"", flakePath),
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

// Read and parse the image config from the json output
func (b *Builder) ParseNix2ContainerConfig(buildResult *client.Result) (config *Nix2ContainerConfig, err error) {
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

// Construct each layer by copying paths from cached nix store
func (b *Builder) ConstructLayers(nixBuilder llb.State, buildResult *client.Result, config Nix2ContainerConfig) ([]LayerResult, error) {
	selfImageSt := GetSelfImage()

	// Create layersSt for each store path
	var layers []LayerResult
	for _, layer := range config.Layers {
		layerJsonBytes, err := json.Marshal(layer)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize layer: %w", err)
		}

		// Write layer definition into json input
		layerJsonState := llb.Scratch().
			File(llb.Mkfile("/layer.json", 0644, layerJsonBytes, llb.WithCreatedTime(time.Time{})),
				dockerui.WithInternalName(fmt.Sprintf("%s: preparing manifest", layer.Digest)))

		// Construct layer by copying paths from cached nix store
		layerSt := llb.Scratch().
			File(llb.Mkdir("/layer/nix/store", 0755, llb.WithParents(true), llb.WithCreatedTime(time.Time{})),
				dockerui.WithInternalName("layer base")).
			Run(
				// Mount nix store cache
				llb.AddMount("/src", nixBuilder, llb.AsPersistentCacheDir("nix2container-buildkit-nix-cache", llb.CacheMountShared)),
				// Mount self image with util binaries
				llb.AddMount("/self", selfImageSt),
				// Mount layer definition
				llb.AddMount("/build", layerJsonState),
				llb.Shlexf("/self/construct-layer --config %s --source-prefix %s --dest-prefix %s", "/build/layer.json", "/src", "/layer"),
				dockerui.WithInternalName(fmt.Sprintf("%s: constructing layer", layer.Digest)),
			).Root()

		if IsMergeSupported(b.c) {
			// Rebase /layer as new root / to enable efficient merge later
			// For copy based strategy there is no need to do this as it's just extra copy
			layerSt = llb.Scratch().
				File(llb.Copy(layerSt, "/layer", "/", &llb.CopyInfo{
					CopyDirContentsOnly: true,
				}), dockerui.WithInternalName(fmt.Sprintf("%s: storing layer", layer.Digest)))
		}

		layers = append(layers, LayerResult{
			State: layerSt,
			Info:  layer,
		})
	}

	return layers, nil
}

// Combine layers into final image state
func (b *Builder) CombineLayers(layers []LayerResult) (*client.Result, error) {
	var finalState llb.State
	if IsMergeSupported(b.c) {
		// Create final state by merging layers together
		states := []llb.State{}
		for _, layer := range layers {
			states = append(states, layer.State)
		}
		finalState = llb.Merge(states)
	} else {
		// Create final state by copying layers on top of each other
		finalState = llb.Scratch()
		for _, layer := range layers {
			// Copy /layer contents into final image
			finalState = finalState.File(
				llb.Copy(layer.State, "/layer", "/", &llb.CopyInfo{
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
